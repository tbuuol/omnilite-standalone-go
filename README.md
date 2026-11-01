# OmniLite Standalone

Eigenständiger OmniLite-Indexer (und später Wallet), der gegen jeden aktuellen `litecoind`
läuft, statt an einen gepatchten Litecoin Core gebunden zu sein. Details und Phasen: [Plan.md](Plan.md).

**Stand:** Phase 0 bis 2. Der Indexer folgt der Chain ab dem Omni-Genesis-Block, behandelt Reorgs
und dekodiert Omni-Transaktionen (Class A/B/C, Sender, Referenz, Payload). Balances und Properties
werden noch nicht berechnet. Datenbanken aus Phase 1 müssen gelöscht und neu aufgebaut werden.

## Voraussetzungen

- Go 1.26 oder neuer
- `litecoind` mit `-txindex=1`, nicht gepruned

## Bauen und starten

```
go build ./cmd/omnilite-standalone
omnilite-standalone -check      # Verbindung, Chain und txindex prüfen
omnilite-standalone             # synchronisieren (Strg+C beendet)
```

Konfiguration: siehe [omnilite-standalone.conf.example](omnilite-standalone.conf.example).

## Tests

```
go test ./...
```

## Lizenz

MIT
