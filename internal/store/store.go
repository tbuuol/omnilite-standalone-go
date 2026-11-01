// Package store persists the processed chain and the Omni state in SQLite.
//
// Every block is written in one transaction. Each state change records the
// previous value in an undo journal, so the tip block can be disconnected
// again during a reorg. Undo data older than the configured depth is pruned.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS blocks (
	height    INTEGER PRIMARY KEY,
	hash      TEXT NOT NULL UNIQUE,
	prev_hash TEXT NOT NULL,
	time      INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS state (
	ns    TEXT NOT NULL,
	key   TEXT NOT NULL,
	value BLOB NOT NULL,
	PRIMARY KEY (ns, key)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS undo (
	seq    INTEGER PRIMARY KEY AUTOINCREMENT,
	height INTEGER NOT NULL,
	ns     TEXT NOT NULL,
	key    TEXT NOT NULL,
	existed INTEGER NOT NULL, -- 0: key did not exist before the change
	old    BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS undo_height ON undo (height);
`

var (
	// ErrNotOnTip is returned when a block does not extend the current tip.
	ErrNotOnTip = errors.New("block does not extend the current tip")
	// ErrUndoUnavailable is returned when a reorg goes deeper than the kept
	// undo data. The database has to be rebuilt in that case.
	ErrUndoUnavailable = errors.New("no undo data for this block (reorg deeper than undodepth)")
	// ErrEmpty is returned when disconnecting from an empty store.
	ErrEmpty = errors.New("store is empty")
)

type Block struct {
	Height   int64
	Hash     string
	PrevHash string
	Time     int64
}

type Store struct {
	db        *sql.DB
	undoDepth int64
}

// Open opens or creates the database at path. network is stored on first
// use and checked afterwards, so a mainnet database is never fed testnet blocks.
func Open(path, network string, undoDepth int64) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// One connection: all writes are serialized anyway, and it keeps
	// transactions and pragmas on the same connection.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, undoDepth: undoDepth}
	if err := s.init(network); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init(network string) error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	for key, want := range map[string]string{"schema_version": strconv.Itoa(schemaVersion), "network": network} {
		var have string
		err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&have)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)`, key, want); err != nil {
				return err
			}
		case err != nil:
			return err
		case have != want:
			return fmt.Errorf("database has %s %q, expected %q", key, have, want)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Tip returns the last processed block, or nil if the store is empty.
func (s *Store) Tip(ctx context.Context) (*Block, error) {
	return scanBlock(s.db.QueryRowContext(ctx,
		`SELECT height, hash, prev_hash, time FROM blocks ORDER BY height DESC LIMIT 1`))
}

// BlockAt returns the processed block at height, or nil.
func (s *Store) BlockAt(ctx context.Context, height int64) (*Block, error) {
	return scanBlock(s.db.QueryRowContext(ctx,
		`SELECT height, hash, prev_hash, time FROM blocks WHERE height = ?`, height))
}

// Get reads a committed state value.
func (s *Store) Get(ctx context.Context, ns, key string) ([]byte, bool, error) {
	return get(ctx, s.db, ns, key)
}

// Begin starts writing block b, which must extend the current tip.
func (s *Store) Begin(ctx context.Context, b Block) (*BlockTx, error) {
	tip, err := s.Tip(ctx)
	if err != nil {
		return nil, err
	}
	if tip != nil && (b.Height != tip.Height+1 || b.PrevHash != tip.Hash) {
		return nil, fmt.Errorf("%w: tip %d %s, block %d prev %s", ErrNotOnTip, tip.Height, tip.Hash, b.Height, b.PrevHash)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blocks (height, hash, prev_hash, time) VALUES (?, ?, ?, ?)`,
		b.Height, b.Hash, b.PrevHash, b.Time); err != nil {
		tx.Rollback()
		return nil, err
	}
	if tip == nil {
		// Undo data exists from the very first block on.
		if err := setMeta(ctx, tx, "undo_floor", b.Height); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	return &BlockTx{ctx: ctx, tx: tx, height: b.Height, undoDepth: s.undoDepth}, nil
}

// Disconnect removes the tip block and reverts all state changes it made.
// It returns the removed block.
func (s *Store) Disconnect(ctx context.Context) (*Block, error) {
	tip, err := s.Tip(ctx)
	if err != nil {
		return nil, err
	}
	if tip == nil {
		return nil, ErrEmpty
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	floor, err := getMeta(ctx, tx, "undo_floor")
	if err != nil {
		return nil, err
	}
	if tip.Height < floor {
		return nil, ErrUndoUnavailable
	}

	rows, err := tx.QueryContext(ctx, `SELECT ns, key, existed, old FROM undo WHERE height = ? ORDER BY seq DESC`, tip.Height)
	if err != nil {
		return nil, err
	}
	type entry struct {
		ns, key string
		existed bool
		old     []byte
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.ns, &e.key, &e.existed, &e.old); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, e := range entries {
		if !e.existed {
			_, err = tx.ExecContext(ctx, `DELETE FROM state WHERE ns = ? AND key = ?`, e.ns, e.key)
		} else {
			if e.old == nil {
				e.old = []byte{} // the driver reads an empty BLOB back as nil
			}
			_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO state (ns, key, value) VALUES (?, ?, ?)`, e.ns, e.key, e.old)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM undo WHERE height = ?`, tip.Height); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blocks WHERE height = ?`, tip.Height); err != nil {
		return nil, err
	}
	return tip, tx.Commit()
}

// BlockTx collects the state changes of one block.
type BlockTx struct {
	ctx       context.Context
	tx        *sql.Tx
	height    int64
	undoDepth int64
}

func (t *BlockTx) Height() int64 { return t.height }

// Get reads a value including the changes made so far in this block.
func (t *BlockTx) Get(ns, key string) ([]byte, bool, error) {
	return get(t.ctx, t.tx, ns, key)
}

// Put sets a state value and journals the previous one.
func (t *BlockTx) Put(ns, key string, value []byte) error {
	if value == nil {
		value = []byte{}
	}
	if err := t.journal(ns, key); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT OR REPLACE INTO state (ns, key, value) VALUES (?, ?, ?)`, ns, key, value)
	return err
}

// Delete removes a state value and journals the previous one.
func (t *BlockTx) Delete(ns, key string) error {
	if err := t.journal(ns, key); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(t.ctx, `DELETE FROM state WHERE ns = ? AND key = ?`, ns, key)
	return err
}

func (t *BlockTx) journal(ns, key string) error {
	old, existed, err := t.Get(ns, key)
	if err != nil {
		return err
	}
	if old == nil {
		old = []byte{}
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO undo (height, ns, key, existed, old) VALUES (?, ?, ?, ?, ?)`, t.height, ns, key, existed, old)
	return err
}

// Commit writes the block and prunes undo data that fell out of the window.
func (t *BlockTx) Commit() error {
	if floor := t.height - t.undoDepth + 1; floor > 0 {
		cur, err := getMeta(t.ctx, t.tx, "undo_floor")
		if err != nil {
			t.tx.Rollback()
			return err
		}
		if floor > cur {
			if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM undo WHERE height < ?`, floor); err != nil {
				t.tx.Rollback()
				return err
			}
			if err := setMeta(t.ctx, t.tx, "undo_floor", floor); err != nil {
				t.tx.Rollback()
				return err
			}
		}
	}
	return t.tx.Commit()
}

// Rollback discards the block.
func (t *BlockTx) Rollback() error { return t.tx.Rollback() }

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func get(ctx context.Context, q queryer, ns, key string) ([]byte, bool, error) {
	var v []byte
	err := q.QueryRowContext(ctx, `SELECT value FROM state WHERE ns = ? AND key = ?`, ns, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func getMeta(ctx context.Context, tx *sql.Tx, key string) (int64, error) {
	var v string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func setMeta(ctx context.Context, tx *sql.Tx, key string, v int64) error {
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, key, strconv.FormatInt(v, 10))
	return err
}

func scanBlock(row *sql.Row) (*Block, error) {
	var b Block
	err := row.Scan(&b.Height, &b.Hash, &b.PrevHash, &b.Time)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}
