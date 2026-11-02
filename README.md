# OmniLite Standalone

Eigenständiger OmniLite-Indexer (und später Wallet), der gegen jeden aktuellen `litecoind`
läuft, statt an einen gepatchten Litecoin Core gebunden zu sein. Details und Phasen: [Plan.md](Plan.md).

**Stand:** Phase 0 bis 5. Der Indexer folgt der Chain ab dem Omni-Genesis-Block, behandelt Reorgs,
berechnet den vollständigen Omni-State wie OmniLite 0.18.1 und bietet eine JSON-RPC-API mit den
`omni_*`-Aufrufen von `omnilitecored`. Datenbanken älterer Phasen müssen gelöscht und neu aufgebaut werden.

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

## API

Der Indexer lauscht standardmäßig auf `127.0.0.1:9432` (Mainnet, `rpcport + 100`) und spricht JSON-RPC
wie `omnilitecored`. Zugang über `apiuser`/`apipassword` oder, ohne Passwort, über die Cookie-Datei
`<datadir>/<network>/.cookie`.

```
curl --user omni:change-me -d '{"method":"omni_getproperty","params":[3]}' http://127.0.0.1:9432
```

Neben den `omni_*`-Aufrufen werden lesende Litecoin-Aufrufe wie `getrawtransaction` an `litecoind`
weitergereicht. Für liteworlds.quest reicht es, in `secure.php` Port und Zugangsdaten auf den Indexer
zu ändern (oder `apiport=10003` zu setzen).

## Tests

```
go test ./...
```

## Lizenz

MIT
