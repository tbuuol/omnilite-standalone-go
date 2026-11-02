# OmniLite Standalone

Eigenständiger OmniLite-Indexer (und später Wallet), der gegen jeden aktuellen `litecoind`
läuft, statt an einen gepatchten Litecoin Core gebunden zu sein. Details und Phasen: [Plan.md](Plan.md).

**Stand:** Phase 0 bis 4. Der Indexer folgt der Chain ab dem Omni-Genesis-Block, behandelt Reorgs,
dekodiert Omni-Transaktionen und berechnet den vollständigen Omni-State: Balances, Properties,
Crowdsales, STO, DEx, Freeze, NFTs, Activations und Alerts, dazu den Consensus-Hash von OmniLite.
Eine Abfrage-API folgt in Phase 5. Datenbanken älterer Phasen müssen gelöscht und neu aufgebaut werden.

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
