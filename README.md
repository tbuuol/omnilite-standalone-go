# OmniLite Standalone

Eigenständiger OmniLite-Indexer, der gegen jeden aktuellen `litecoind` läuft, statt an einen
gepatchten Litecoin Core gebunden zu sein. Einzelanwender können damit auch Omni-Transaktionen senden,
über die Wallet ihres `litecoind`. Eine eigene Wallet gibt es nicht. Details und Phasen: [Plan.md](Plan.md).

**Stand:** Phase 0 bis 8. Der Indexer folgt der Chain ab dem Omni-Genesis-Block, behandelt Reorgs,
berechnet den vollständigen Omni-State wie OmniLite 0.18.1 und bietet eine JSON-RPC-API mit den
`omni_*`-Aufrufen von `omnilitecored`, auch für unbestätigte Transaktionen aus dem Mempool. Mit `wallet=1`
sendet er Token und verwaltet Properties über die Node-Wallet. Datenbanken älterer Phasen müssen gelöscht und neu aufgebaut werden.

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

## Senden

Mit `wallet=1` stehen `omni_send`, `omni_sendall`, `omni_sendnonfungible`, `omni_sendanydata` und
`omni_sendrawtx` bereit, für die Token-Verwaltung `omni_sendissuancefixed`, `_sendissuancecrowdsale`,
`_sendissuancemanaged`, `_sendgrant`, `_sendrevoke`, `_sendclosecrowdsale`, `_sendchangeissuer`,
`_sendenablefreezing`, `_senddisablefreezing`, `_sendfreeze`, `_sendunfreeze`, `_sendsto` und
`omni_setnonfungibledata`. Coins, Schlüssel und Signatur kommen von der Wallet des `litecoind`
(bei mehreren Wallets `walletname=`). Jede Transaktion wird vorher gegen den eigenen Omni-State
geprüft und nur gesendet, wenn sie gültig wäre.

```
litecoin-cli getnewaddress "" p2sh-segwit        # neue M...-Adresse
litecoin-cli walletpassphrase "<passphrase>" 60  # bei verschlüsselter Wallet
curl --user omni:change-me -d '{"method":"omni_send","params":["M...","M...",3,"10.5"]}' http://127.0.0.1:9432
```

Ein vorhandener Schlüssel, etwa von liteworlds.quest, wird in die Node importiert (`importprivkey`,
bei Descriptor-Wallets `importdescriptors`). Wer den Indexer als Provider betreibt, lässt `wallet=0`.
Eine API mit `wallet=1` gehört nicht offen ins Netz.

## Tests

```
go test ./...
```

## Lizenz

MIT
