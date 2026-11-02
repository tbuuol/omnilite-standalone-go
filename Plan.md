# OmniLite Standalone – Plan

**OmniLite Standalone** ist ein eigenständiger OmniLite-Indexer, der **nicht** an eine bestimmte Litecoin-Core-Version gekoppelt ist.
Er verbindet sich per Standard-RPC mit einem beliebigen, aktuellen `litecoind`, liest die Blöcke,
dekodiert Omni-Transaktionen selbst, berechnet den kompletten Omni-State (Balances, Properties,
Crowdsales, DEx, NFTs, Freeze, Activations) und stellt ihn über eine API bereit.

Für Einzelanwender kann er außerdem **Omni-Transaktionen senden**: Token erstellen, senden und
verwalten und am DEx handeln. Er baut die Omni-Transaktionen selbst und prüft sie gegen den eigenen
State. Schlüssel, Signieren und Coins bleiben bei der **Wallet des `litecoind`**. Eine eigene Wallet hat
OmniLite Standalone nicht. Als Provider (z. B. für eine Website) läuft er als reiner Indexer und liefert
nur Daten aus dem Omni-Index.

**Kernidee:** Die Omni-Logik lebt außerhalb von Core. Core-Updates (0.21, MWEB, alles danach) betreffen
uns nur noch, wenn sich die Standard-RPC-Schnittstelle ändert – und die ist sehr stabil.

---

## 1. Ziele und Nicht-Ziele

**Ziele**
- Konsens-identisch mit OmniLite 0.18.1 (`omnilitecored`) – bit-genau, nachweisbar per Consensus-Hash.
- Läuft gegen jeden `litecoind` ab 0.18 (Ziel: aktuelles 0.21.x).
- Reorg-sicher.
- `omni_*`-kompatible JSON-RPC-Schnittstelle (Teilmenge), damit bestehende Tools und die
  LiteWorlds-Website nur den Endpoint tauschen müssen.
- Ein Binary, einfache Konfiguration, eingebettete Datenbank.
- Senden: alle Omni-Aktionen, die `omnilitecored` kann (Send, Issuance, Grant/Revoke, Crowdsale,
  STO, Freeze, NFT, DEx Sell/Accept/Pay), über die Wallet des Nodes (`wallet=1`).
- Keine verbrannten Gebühren: Jede Omni-Tx wird vor dem Senden gegen den eigenen State geprüft.

**Nicht-Ziele (vorerst)**
- Keine eigene P2P-Anbindung (nur RPC).
- Keine eigene Wallet: keine Schlüssel, kein Seed, kein Keystore. Das macht die Wallet des Nodes.
- Keine Protokolländerungen. Wir bilden 0.18.1 nach, Fehler eingeschlossen.

---

## 2. Fakten aus OmniLite 0.18.1 (Referenz)

Quelle: `github.com/OmniLite/omnilite`, Branch `master` = 0.18.1, letzter Commit 2022-05-18.

| Parameter | Mainnet |
|---|---|
| `GENESIS_BLOCK` | 2 010 500 |
| Exodus-Adresse | `LTceXoduS2cetpWJSe47M25i5oKjEccN1h` |
| Class-C-Marker | `0x6f6d6e69` ("omni") im OP_RETURN |
| `MSC_NONFUNGIBLE_BLOCK` | 2 262 000 |
| `MSC_STOV1_BLOCK`, `FREEZENOTICE_FEATURE_BLOCK` | 100 000 000 (= nicht aktiv, außer per Activation) |
| `FREEDEX_FEATURE_BLOCK` | = Genesis |
| Mainnet-Consensus-Checkpoints | **keine** (leere Liste in `rules.cpp`) |

**Transaktionstypen** (`omnicore.h`): 0 Simple Send, 2 Restricted Send, 3 STO, 4 Send All,
5 Send NFT, 20 DEx Sell Offer, 22 DEx Accept,
31 Notification, 50 Fixed Issuance, 51 Crowdsale, 53 Close Crowdsale, 54 Managed Issuance,
55 Grant, 56 Revoke, 70 Change Issuer, 71/72 Enable/Disable Freezing, 185/186 Freeze/Unfreeze,
200 Any Data, 201 NFT Data, 65533 Deactivation, 65534 Activation, 65535 Alert.

**Kein MetaDEx:** OmniLite hat `mdex.cpp` und die Typen 25–28 entfernt. Gehandelt wird nur über den
klassischen DEx (Token gegen LTC), mit `FREEDEX` für beliebige Properties.

**Erlaubte Adresstypen** (`rules.cpp`, `IsAllowedInputType` / `IsAllowedOutputType`):
- Inputs (Sender): nur **P2PKH** (`L…`) und **P2SH** (`M…`). Bech32-Adressen (`ltc1…`) können
  **keine** Omni-Sender sein.
- Outputs: P2PKH, P2SH, Bare-Multisig (Class B), OP_RETURN (Class C). Auch Empfänger müssen also
  `L…` oder `M…` sein.

**DEx-Zahlung** (`HandleDExPayments`): Das ist eine Class-A-Tx ohne Payload, also eine normale
LTC-Transaktion mit einem Output an die Exodus-Adresse und Outputs an den Seller. Der Sender
(= Käufer) wird über die größte Input-Summe bestimmt.

**Sender-Bestimmung** (`omnicore.cpp`, `parseTransaction`):
- Class C: Sender = Adresse des **ersten Inputs**.
- Class A/B: Sender = Adresse mit der **größten Input-Summe** („largest input by sum").
- Referenzadresse: Spezialregeln (erster Output an Sender wird als Change ignoriert usw.) → 1:1 portieren.

Da es keine Mainnet-Checkpoints gibt, müssen wir die Referenzwerte selbst erzeugen (siehe Abschnitt 6).

---

## 3. Architektur

```
 litecoind (aktuell, -txindex)                        Clients
        │  JSON-RPC: getblockhash, getblock(v2),        (Website, Explorer, Tools)
        │  getrawtransaction, getrawmempool                    ▲
        │                                                      │ JSON-RPC (omni_*)
        ▼                                                      │ Senden nur mit wallet=1
 ┌───────────────┐   ┌─────────────┐   ┌──────────────┐   ┌────┴─────┐
 │ Block Fetcher │──▶│ Tx Decoder  │──▶│ State Engine │──▶│   API    │
 │ + Mempool     │   │ Class A/B/C │   │ (Konsens)    │   └────┬─────┘
 │ + Reorgs      │   │ Sender/Ref  │   │ Tally, SP,   │        │
 └───────────────┘   │ Payload     │   │ DEx, STO,    │   ┌────▼─────────────┐
                     └─────────────┘   │ NFT ...      │◀──│ Tx-Builder       │
                                       └──────┬───────┘   │ + Dry-Run        │
                                              │           └────┬─────────────┘
                                              ▼                │ listunspent, getaddressinfo,
                                       SQLite (eingebettet)    │ signrawtransactionwithwallet,
                                       State + Tx-Liste +      ▼ sendrawtransaction
                                       Undo-Journal        Wallet des litecoind
```

**Sprache: Go.** Gründe: ein statisches Binary für Windows und Linux, gute Unterstützung für
JSON-RPC und HTTP, Integer-Arithmetik ohne Überraschungen (`int64`, `math/big` für 128-Bit-Zwischenwerte
wie in `uint256_extensions.h`), SQLite ohne cgo (`modernc.org/sqlite`).

**Datenquelle:** `getblock <hash> 2` liefert alle Txs dekodiert. Für die Sender-Bestimmung brauchen wir
die Prevouts der Inputs → `getrawtransaction` mit `-txindex` am Node. Nur Omni-Kandidaten
(Exodus-Output oder Marker) und DEx-Zahlungen lösen Prevout-Lookups aus, das hält die Last klein.
Option für später: eigener Prevout-Cache, dann ist `-txindex` nicht mehr nötig.

**Persistenz:** SQLite, ein Write-Transaction pro Block. Jede State-Änderung schreibt einen
Eintrag ins Undo-Journal (`block_height`, Tabelle, Schlüssel, alter Wert). Bei einem Reorg rollen wir
blockweise bis zum gemeinsamen Vorfahren zurück. Undo-Daten älter als N Blöcke (Default 100) werden
gelöscht.

### Senden über die Node-Wallet

OmniLite Standalone hat **keine eigene Wallet**. Wer senden will, nutzt die Wallet seines `litecoind`.
Sie hält die Schlüssel, kennt die Coins, signiert und verschlüsselt (`walletpassphrase`). Das ist
sauberer, als neben der vorhandenen Wallet eine zweite anzuhängen.

**Adressen**
- Neue Adressen kommen von der Node: `getnewaddress "" p2sh-segwit` ergibt `M…` (P2SH-P2WPKH), den
  Standard-Adresstyp von OmniLite 0.18.1 und der LiteWorlds-Wallet.
- Kein Bech32 (OmniLite akzeptiert das nicht als Sender oder Referenz). Der Builder nimmt nur `L…` und
  `M…` an.
- Schlüssel von liteworlds.quest oder alte `L…`-Adressen werden in die Node importiert
  (`importprivkey`, bei Descriptor-Wallets `importdescriptors` mit `sh(wpkh(<WIF>))` bzw. `pkh(<WIF>)`).

**Tx-Builder** (Port von `wallettxbuilder.cpp`, `walletutils.cpp`, `encoding.cpp`, `createtx.cpp`)
1. Payload bauen (rein, ohne Node).
2. Encoding wählen: Class C (OP_RETURN mit `omni`-Marker), wenn Payload, Marker, OP_RETURN und zwei
   Push-Bytes in 83 Byte passen (Payload ≤ 76 Byte). Sonst Class B (verschleiertes Bare-Multisig mit dem
   Schlüssel des Senders, dahinter der Exodus-Output).
3. Inputs: nur Coins der Sender-Adresse aus `listunspent` (sicher, ausgebbar, über der
   Wirtschaftlichkeitsgrenze), bis die Gebühr für 20 kB plus Referenzbetrag gedeckt ist (`SelectCoins`).
   So ist der erste Input vom Sender (Class C) und der Sender stellt die größte Input-Summe (Class B).
4. Outputs: Change an den Sender an erster Stelle (wie `omni_createrawtx_change`), dann die Daten, die
   Referenz an letzter Stelle. Die Referenzregel überspringt den ersten Output an den Sender.
5. Gebühr: `estimatesmartfee` für 6 Blöcke, sonst `fallbackfee`, mindestens die Relay-Gebühr. Dust-Grenzen
   wie OmniLite (`OmniGetDustThreshold`, 30-fache Relay-Dust-Grenze).
6. **Dry-Run:** Die fertige Tx läuft durch den eigenen Decoder (Sender, Referenz und Payload müssen
   stimmen) und die State Engine, auf einer Kopie des aktuellen States plus der eigenen noch
   unbestätigten Txs. Ist sie Omni-ungültig, wird **nicht** gesendet. Der Index muss dafür auf der Höhe
   des Nodes sein.
7. `signrawtransactionwithwallet`, `sendrawtransaction`, als Pending vormerken.

**Pending**
- Eigene unbestätigte Txs zählen sofort gegen den Token-Bestand (Pending-Abzug und Dry-Run). Die Coins
  sperrt die Node-Wallet selbst, sobald die Tx im Mempool ist.
- Verlässt die Tx den Mempool oder steht sie in einem Block, fällt sie aus der Pending-Liste.
- RBF und CPFP für hängende Txs (später).

**Zugriff**
- Senden ist standardmäßig aus (`wallet=0`). Für einen Provider bleibt das so: reiner Indexer.
- Mit `wallet=1` hängen die `omni_send*`-Aufrufe an der normalen API. Die lauscht standardmäßig nur auf
  `127.0.0.1`. Wer senden kann, darf die API nicht offen ins Netz stellen.
- Die Website spricht nur mit einem Indexer ohne `wallet=1`. Signiert wird nie auf dem Webserver.

---

## 4. Modul-Mapping (Omni Core → Indexer)

| Omni Core (`src/omnicore/`) | Indexer-Paket | Inhalt |
|---|---|---|
| `omnicore.cpp` (`parseTransaction`, `HandleDExPayments`) | `decode` | Klassen-Erkennung, Sender/Referenz, Class-B-Entschleierung (SHA256-Kette), Class-C-Payload, DEx-Zahlungen |
| `tx.cpp`, `tx.h` | `consensus/tx` | Payload-Parsing und `logicMath_*` je Tx-Typ, inkl. aller Fehlercodes |
| `tally.cpp` | `state/tally` | Balances (Balance, Selling-, Accept-Reserve) |
| `sp.cpp` | `state/property` | Properties, Crowdsales, Grants/Revokes |
| `dex.cpp` | `state/dex` | Sell Offers, Accepts, Payment Window, Expiry |
| `sto.cpp` | `state/sto` | Send-to-Owners-Verteilung |
| `nftdb.cpp` | `state/nft` | NFT-Ranges, Ownership, Data |
| `activation.cpp`, `rules.cpp` | `consensus/rules` | Feature-Activations, Alerts, Block-Parameter |
| `consensushash.cpp` | `consensus/hash` | Consensus-Hash, identisch zur Referenz |
| `rpc*.cpp`, `rpctxobject.cpp` | `api` | `omni_*`-JSON-Format |
| `createpayload.cpp`, `rpcpayload.cpp` | `omni/create`, `api/wallet` | Payloads für alle Tx-Typen |
| `encoding.cpp`, `script.cpp` | `omni/encode`, `wallet/fee` | Class B (Multisig + Verschleierung) und Class C (OP_RETURN), Dust |
| `wallettxbuilder.cpp`, `walletutils.cpp`, `createtx.cpp` | `wallet` | Input-Auswahl, Referenz, Change, Gebühr, Dry-Run |
| `rpctx.cpp`, `rpcrawtx.cpp` | `api/wallet` | `omni_send*`, `omni_createrawtx_*` |
| `pending.cpp` | `state/pending` | Unbestätigte Txs im State |
| `test/*.cpp` | `*_test.go` | Unit-Tests portieren |

Grundsatz: **Portieren, nicht neu erfinden.** Jede Funktion bekommt im Kommentar die Quelldatei und
Zeile in OmniLite 0.18.1, aus der sie stammt.

---

## 5. Phasen

### Phase 0 – Grundgerüst
- Go-Modul, Config (RPC-URL, User/Pass, DB-Pfad, Netzwerk), Logging.
- RPC-Client für `litecoind`, Prüfung auf `-txindex`.
- **Fertig, wenn:** `omnilite-standalone --check` meldet Node-Version, Höhe und txindex-Status.

### Phase 1 – Block-Pipeline und Reorgs
- Blöcke ab `GENESIS_BLOCK` lesen, Höhe und Hash in der DB speichern, Tip folgen (Polling oder ZMQ).
- Reorg-Erkennung über `previousblockhash`, Rollback über das Undo-Journal (zunächst mit leerem State).
- **Fertig, wenn:** Sync bis zum Tip läuft und ein simulierter Reorg im Regtest sauber zurückrollt.
- **Stand:** umgesetzt. Reorgs (länger, kürzer, gleiche Höhe, unter den Startblock, tiefer als `undodepth`)
  sind mit einer simulierten Chain getestet. Ein Lauf gegen einen echten `litecoind` (Mainnet-Sync,
  Regtest-Reorg mit `invalidateblock`) steht noch aus.

### Phase 2 – Decoder
- Kandidatenerkennung (Exodus-Output, Class-C-Marker), Class A/B/C, Sender und Referenz, Payload.
- Ausgabe: Liste aller Omni-Kandidaten mit Typ/Version/Rohpayload.
- **Fertig, wenn:** die Kandidatenliste für den gesamten Mainnet-Bereich exakt der Liste aus
  `omni_listblocktransactions` des Referenz-Nodes entspricht (gleiche Txids, gleiche Sender).
- **Stand:** umgesetzt (`internal/omni`, `internal/indexer`). Klassenerkennung, Sender (Class C: erster
  Input; A/B: größte Input-Summe, bei Gleichstand die kleinste Adresse wie `std::map`), Referenz,
  Class-B-Entschleierung und Class-C-Payload sind aus `parseTransaction` portiert. Die strukturelle
  Prüfung (`interpret_Transaction`) gehört schon dazu, weil OmniLite Txs mit kaputtem Payload gar nicht
  erst speichert; sonst wäre die Liste nicht vergleichbar. Class-A-Txs ohne Daten werden als mögliche
  DEx-Zahlung mit ihren Outputs gespeichert (für Phase 4). Getestet mit gebauten Txs; der Abgleich mit
  `omni_listblocktransactions` steht aus, bis ein Referenz-Node läuft.

### Phase 3 – Basis-State
- Simple Send, Send All, Restricted Send, Issuance (fixed/managed), Grant/Revoke, Change Issuer,
  Activations/Deactivations/Alerts, Any Data.
- Gültig/Ungültig mit identischen Fehlercodes speichern.
- **Fertig, wenn:** Balances und Properties bis zum Tip mit dem Referenz-Node übereinstimmen
  (für Blöcke, in denen nur diese Typen vorkommen).
- **Stand:** umgesetzt (`internal/state`): Simple Send, Send All, Issuance (fixed/managed), Grant/Revoke, Change
  Issuer, Any Data, Activations/Deactivations (inkl. Live-Schaltung am Aktivierungsblock) und Alerts, mit
  den Fehlercodes von OmniLite. Restricted Send gibt es in OmniLite nicht (kein Interpreter, Typ 2 ist
  strukturell ungültig). Nachgebaute Eigenheiten: `getIssuer` vergleicht nur Blockhöhen (ein Issuer-Wechsel
  später im selben Block zählt schon), Change Issuer auf Property 1/2 ändert nichts, Deaktivierung von
  Feature 18 setzt Block 999999. Typen aus Phase 4 werden als „unsupported“ gespeichert; ab der ersten
  solchen Tx gilt der State als unvollständig (`-check` zeigt das an). Grants auf NFT-Properties buchen die
  Balance, die Token-Ranges fehlen noch. Für Regtest gibt es `omniactivationallowsender` und
  `omnialertallowsender`. Der Abgleich mit dem Referenz-Node steht aus.

### Phase 4 – Komplexe Logik
- Crowdsales (inkl. Early Bonus, Issuer-Anteil, Deadline, Close), STO (v0 und v1),
  DEx (Offer, Accept, Zahlung, Expiry; mit FREEDEX für beliebige Properties), Freeze/Unfreeze, NFTs.
- **Fertig, wenn:** der Consensus-Hash an **jeder** Blockhöhe ab Genesis mit der Referenz identisch ist.
- **Stand:** umgesetzt (`internal/state`): Crowdsales (Teilnahme per Simple Send, Early Bird, Issuer-Bonus,
  Max-Tokens, Ablauf am Blockanfang, Close mit Missed-Bonus), STO v0/v1, DEx (Offer v0/v1, Accept mit
  Mindestgebühr, Zahlung über Class A, Ablauf am Blockende, FREEDEX), Freeze/Unfreeze inkl. Enable/Disable,
  NFTs (Ranges beim Grant, Unique Send, Issuer-/Holder-Daten) und der Consensus-Hash (`-check`,
  `consensushash=N` loggt ihn alle N Blöcke). Rechenwege und Testvektoren stammen aus den OmniLite-Tests
  (`crowdsale_participation_tests`, `dex_purchase_tests`, `nftdb_tests`). Nachgebaute Eigenheiten:
  - Im Consensus-Hash wird der Verkäufer eines Offers durch Abschneiden von zwei Zeichen aus `addr-prop`
    gewonnen; ab Property 10 steht dort also ein Teil der Property-ID mit drin.
  - Bei einer DEx-Zahlung mit FREEDEX zählt nur das (erste) aktuelle Offer des Verkäufers. Ist es
    storniert, findet die Zahlung das noch offene Accept nicht.
  - Ohne vorhandene OMNI-/TOMNI-Tokens kann niemand die STO-Gebühr zahlen; STO ist auf OmniLite also
    praktisch immer ungültig (-50027).
  - NFT-Daten (Typ 201) ersetzen nur Ranges, die ab dem Start lückenlos gefunden werden.
  - Unique Send prüft in OmniLite `GetAvailableTokenBalance`, das Pending-Beträge der eigenen Wallet
    abzieht. Das ist knotenabhängig und hier bewusst weggelassen.
  Der Abgleich des Consensus-Hashes mit dem Referenz-Node steht aus.

### Phase 5 – API
- JSON-RPC-Teilmenge, kompatibel zu `omnilitecored`:
  - Von der Website genutzt: `omni_getproperty`, `omni_getallbalancesforaddress`,
    `omni_getactivedexsells`, (`omni_createpayload_dexsell` → reine Payload-Erzeugung, ohne Node machbar).
  - Weitere: `omni_getinfo`, `omni_getbalance`, `omni_getallbalancesforid`, `omni_listproperties`,
    `omni_gettransaction`, `omni_listblocktransactions`, `omni_getcrowdsale`, `omni_getactivecrowdsales`,
    `omni_getgrants`, `omni_getsto`, `omni_getactivations`,
    `omni_getcurrentconsensushash`, `omni_getnonfungibletokens`, `omni_getnonfungibletokendata`.
  - Antwortformat byte-kompatibel (Feldnamen, Strings für Beträge, Reihenfolge).
- Optional REST (`/address/{addr}`, `/property/{id}`, `/tx/{txid}`) für das Explorer-Frontend.
- **Fertig, wenn:** die Website mit dem Indexer statt `omnilitecored` funktioniert.
- **Stand:** umgesetzt (`internal/api`): JSON-RPC-Server (Basic Auth, Batch, HTTP-Statuscodes wie Litecoin Core)
  mit `omni_getinfo`, `getbalance`, `getallbalancesforid`, `getallbalancesforaddress`, `getproperty`,
  `listproperties`, `getcrowdsale`, `getactivecrowdsales`, `getgrants`, `getactivedexsells`,
  `listblocktransactions`, `listblockstransactions`, `gettransaction`, `getsto`, `getactivations`,
  `getcurrentconsensushash`, `getnonfungibletokens`, `getnonfungibletokendata`, `getnonfungibletokenranges`
  und `createpayload_dexsell`. Feldreihenfolge, Zahlenformate und `invalidreason`-Texte (aus `errors.h`)
  wie OmniLite. Lesende Litecoin-Aufrufe (`getrawtransaction`, `getblock`, …) werden an `litecoind`
  durchgereicht, damit die Website nur Host/Port/Zugang tauschen muss. Ohne `apipassword` wird ein
  Cookie geschrieben. Abweichungen: `omni_getallbalancesforid` sortiert nach Adresse (OmniLite: zufällige
  Reihenfolge), unbekannte Txids melden „No Omni Layer Protocol transaction“ statt „No information
  available about transaction“, `omni_getnonfungibletokendata` liefert höchstens 100 000 Tokens pro Aufruf,
  `ismine` ist immer false (keine Wallet). Unbestätigte Txs folgen mit Phase 6. REST ist offen.
  Getestet mit eigenen Fixtures; der Test der Website gegen den laufenden Indexer steht aus.

### Phase 6 – Mempool und Pending
- Mempool über `getrawmempool` und ZMQ (`rawtx`) verfolgen, Omni-Kandidaten dekodieren.
- Pending-State wie in `pending.cpp`, `omni_listpendingtransactions`.
- **Fertig, wenn:** eine im Regtest gesendete Omni-Tx sofort als Pending erscheint und nach dem Mining
  korrekt in den bestätigten State übergeht (auch über einen Reorg hinweg).
- **Stand:** umgesetzt (`internal/mempool`): Der Mempool wird per `getrawmempool` im `pollinterval` abgefragt,
  neue Txs werden einmal geholt und mit dem Indexer-Decoder geprüft. `omni_listpendingtransactions` und
  `omni_gettransaction` zeigen unbestätigte Txs mit `confirmations: 0` und ohne Gültigkeit, wie OmniLite.
  Unbestätigte DEx-Zahlungen erscheinen nicht (OmniLite: „Unconfirmed transactions are not supported“).
  Pending-Abzüge (`AddPending`) gibt es nur für Txs, die dieses Programm selbst sendet (ab Phase 7); sie
  verschwinden, sobald die Tx in einem Block steht oder den Mempool verlässt. Nach einem Reorg wird eine
  zurückgekehrte Tx neu dekodiert. ZMQ ist noch nicht angebunden, Polling reicht für den Anfang.
  Der Regtest-Durchlauf steht aus.

### Phase 7 – Senden über die Node-Wallet
- Tx-Builder mit Class C und Class B, Coins und Signatur von der Node-Wallet, Dry-Run, Broadcast.
- Erste Aktionen: `omni_send`, `omni_sendall`, `omni_sendnonfungible`, `omni_sendanydata`.
- Zusätzlich rohe Bausteine: `omni_createpayload_*`, `omni_createrawtx_*`, `omni_sendrawtx`.
- **Fertig, wenn:** im Regtest gebaute Txs vom Referenz-Node (`omnilitecored`) als gültig gewertet werden,
  mit identischem Ergebnis in `omni_gettransaction`, und ein absichtlich ungültiger Send vom Dry-Run
  abgefangen wird.
- **Stand:** umgesetzt (`internal/wallet`, `internal/omni/create.go`, `internal/omni/encode.go`,
  `internal/api/wallet.go`). Mit `wallet=1` (und `walletname` bei mehreren Wallets) gibt es `omni_send`,
  `omni_sendall`, `omni_sendnonfungible`, `omni_sendanydata` und `omni_sendrawtx` mit den Prüfungen und
  Fehlermeldungen von OmniLite (`RequireBalance` inkl. Pending, `RequireNonFungibleTokenOwner`,
  Referenzbetrag höchstens 0,01 LTC). Ohne `wallet=1` und für Provider gibt es nur die reinen Bausteine:
  `omni_createpayload_simplesend`, `_sendall`, `_sendnonfungible`, `_anydata` und `omni_createrawtx_opreturn`,
  `_multisig`, `_input`, `_reference`, `_change`. Builder wie oben beschrieben; der Dry-Run lehnt ab, wenn
  der Index nicht auf der Höhe des Nodes ist, wenn Sender, Referenz oder Payload anders erkannt würden
  oder wenn die Tx Omni-ungültig wäre (Meldung mit dem `invalidreason` von OmniLite). Gesendete Txs
  erscheinen sofort in `omni_listpendingtransactions`. Abweichungen von OmniLite: die Change-Position ist
  fest (erster Output) statt zufällig, und Send All, Any Data und Raw-Txs prüfen zusätzlich den Dry-Run.
  Getestet mit einer simulierten Node-Wallet; die Gegenprobe im Regtest gegen `omnilitecored` steht aus.
  Offen: `omni_decodetransaction`, `ismine` über `getaddressinfo`.

### Phase 8 – Token-Verwaltung
- Property erstellen: `omni_sendissuancefixed`, `omni_sendissuancemanaged`, `omni_sendissuancecrowdsale`.
- Verwalten: `omni_sendgrant`, `omni_sendrevoke`, `omni_sendclosecrowdsale`, `omni_sendchangeissuer`,
  `omni_sendenablefreezing`, `omni_senddisablefreezing`, `omni_sendfreeze`, `omni_sendunfreeze`.
- Verteilen: `omni_sendsto`.
- NFTs: Ausgabe über Managed Issuance, Grant mit NFT-Daten, NFT-Daten setzen (Typ 201).
- **Fertig, wenn:** jede Aktion im Regtest einmal durchgespielt und gegen den Referenz-Node geprüft ist.
- **Stand:** umgesetzt (`internal/api/manage.go`, `internal/omni/create.go`). Mit `wallet=1`:
  `omni_sendissuancefixed`, `_sendissuancecrowdsale`, `_sendissuancemanaged`, `_sendsto`, `_sendgrant`,
  `_sendrevoke`, `_sendclosecrowdsale`, `_sendchangeissuer`, `_sendenablefreezing`, `_senddisablefreezing`,
  `_sendfreeze`, `_sendunfreeze` und `omni_setnonfungibledata` (Absender ist der Besitzer der Range bzw. der
  Issuer). Dazu die passenden `omni_createpayload_*` ohne Wallet. Parameter, Prüfungen und Fehlermeldungen
  wie OmniLite (`ParseText` bis 255 Zeichen, nur `previousid` 0, Managed-Typ 1/2/5, `RequireTokenIssuer`,
  `RequireActiveCrowdsale` usw.), inklusive der Eigenheiten: `omni_createpayload_grant` verlangt das Memo,
  `RequireSameEcosystem` vergleicht die Ecosystem-Nummer wie eine Property-ID. Der Dry-Run wendet eigene
  unbestätigte Txs vorher an, so geht z. B. ein Grant auf eine Property, deren Issuance noch im Mempool
  liegt. **Bewusste Abweichung:** Freeze/Unfreeze an `M…`-Adressen schreiben das Versionsbyte 5 statt 50
  in die Payload, weil OmniLite die eigene Form nicht zurücklesen kann (siehe Risiken). Getestet mit
  Payload-Rundläufen durch den eigenen Interpreter und einer simulierten Node-Wallet; die Gegenprobe im
  Regtest steht aus.

### Phase 9 – DEx-Handel
- Verkaufen: `omni_senddexsell`, `omni_sendnewdexorder`, `omni_sendupdatedexorder`, `omni_sendcanceldexorder`.
- Kaufen läuft in zwei Schritten, wie auf liteworlds.quest:
  1. **Reservieren** = `omni_senddexaccept` (Typ 22). Die Token werden beim Seller als „accepted“
     reserviert. Die Mindestgebühr `minacceptfee` des Offers muss eingehalten werden.
  2. **Abschließen** = `omni_senddexpay`: die LTC-Zahlung als Class-A-Tx mit Exodus-Output und Output
     an den Seller. Erst damit wandern die Token zum Käufer.
- **Zahlungs-Wächter:** Nach dem Reservieren läuft ein Payment Window (in Blöcken). Der Wächter
  verfolgt offene Reservierungen und zeigt die verbleibenden Blöcke an. Er warnt vor Ablauf.
  **Er zahlt nie automatisch.** Abschließen ist immer eine bewusste Aktion des Nutzers.
- Referenz für den Ablauf: die vorhandene Umsetzung der Website (`omni.js` für die Payloads,
  `transaction.js` für die Zahlung mit Exodus-Output). Deren Txs dienen auch als Testfälle.
- Teilzahlungen, Zahlung nach Ablauf (LTC weg, keine Token!) und Offers, die sich zwischen Accept
  und Zahlung ändern: im Dry-Run gezielt abfangen.
- **Fertig, wenn:** ein kompletter Handel (Offer, Accept, Pay, Token kommen an) im Regtest zwischen
  zwei Wallets läuft und der Referenz-Node den gleichen Endstand zeigt.

### Phase 10 – Betrieb
- Windows-Dienst und systemd-Unit, Docker-Image.
- Snapshot-Export und -Import (DB-Datei + Höhe + Hash), damit niemand ab Genesis syncen muss.
- Metriken: Höhe, Lag zum Node, Reorgs, Dauer pro Block.

### Phase 11 – Später (optional)
- Eigener Prevout-Cache statt `-txindex`.
- RBF und CPFP für hängende Txs.
- Einfache lokale Web-Oberfläche zum Senden (über die Node-Wallet).
- `omni_send*` mit Ausgabe als PSBT statt Signieren (für externe Signer an der Node-Wallet vorbei).

---

## 6. Verifikation – das Wichtigste

Konsens ist alles. Ein Indexer, der an einer Stelle abweicht, ist wertlos.

1. **Referenz-Node einmalig aufsetzen:** `omnilitecored` 0.18.1 mit `-txindex`, vollständig gesynct.
   Er läuft nur für die Verifikation und zeigt über `-connect` auf den eigenen aktuellen `litecoind`.
2. **Referenzdaten exportieren** (Skript `tools/dump-reference`):
   - `omni_getcurrentconsensushash` für jede Höhe ab Genesis. Er liefert nur den aktuellen Hash,
     deshalb nehmen wir die Werte während eines Syncs auf, z. B. per `-blocknotify`. Alternative:
     Consensus-Hash nur an Blöcken mit Omni-Txs plus alle 1000 Blöcke.
   - Alle Omni-Txs mit `omni_gettransaction` (gültig/ungültig, Grund, Beträge).
   - Endstand: `omni_listproperties`, `omni_getallbalancesforid` je Property, DEx-Zustand, NFTs.
3. **Diff-Tool** (`tools/compare`): vergleicht Indexer und Referenz und meldet den ersten
   abweichenden Block mit der betroffenen Tx.
4. **Unit-Tests** aus `src/omnicore/test/` portieren (Payload-Parsing, Encoding, Rundung, Crowdsale-Mathe, STO).
5. **Regtest-Szenarien:** gezielte Txs (Grenzfälle, ungültige Payloads, Reorgs während eines DEx-Accepts)
   gegen beide Implementierungen laufen lassen.
6. **Wallet-Gegenprobe:** Jede Tx, die unser Builder baut, wird im Regtest von **beiden** ausgewertet:
   unserem Indexer und `omnilitecored`. Umgekehrt dekodiert unser Indexer Txs, die `omnilitecored`
   gebaut hat. Payloads werden Byte für Byte mit `omni_createpayload_*` des Referenz-Nodes verglichen.
7. CI: Unit-Tests bei jedem Push, Vollvergleich gegen den Referenz-Dump als manueller Job.

---

## 7. Risiken und offene Fragen

| Thema | Risiko | Umgang |
|---|---|---|
| MWEB / HogEx (Litecoin 0.21) | 0.18-Nodes sehen MWEB-Daten nicht. Txs, die Peg-out-Outputs der HogEx ausgeben, könnten anders erscheinen. | Bei der Kandidatenerkennung alles ignorieren, was 0.18 nicht sieht. Gezielter Test mit Peg-out-Inputs. |
| Adresstypen | Nur P2PKH/P2SH als Sender. Wer Token an `ltc1…` schickt, verliert sie praktisch. | Builder nimmt nur `L…`/`M…` an, neue Adressen per `getnewaddress "" p2sh-segwit`. P2SH-P2WPKH als Sender ist im Mainnet erprobt (LiteWorlds nutzt es), trotzdem in die Regtest-Gegenprobe aufnehmen. |
| Verlorene Gebühren | Omni-ungültige Txs landen trotzdem in der Chain. | Pflicht-Dry-Run vor jedem Broadcast, Pending zählt mit. |
| DEx-Zahlung | Zu spät, zu wenig oder an den falschen Seller gezahlt → LTC weg. | Zahlungs-Wächter, Betrag und Window im Dry-Run prüfen, nur über `omni_senddexpay` zahlen. |
| Schlüssel | Fremde senden über eine offene API mit `wallet=1`. | Senden ist standardmäßig aus, API lauscht nur auf `127.0.0.1` mit Auth. Schlüssel bleiben in der Node-Wallet, die gesperrt sein kann (`walletpassphrase`). |
| Node-Wallet-RPCs | Wallet-Aufrufe ändern sich zwischen Core-Versionen eher als die Block-RPCs. | Nur `listunspent`, `getaddressinfo`, `signrawtransactionwithwallet`, `sendrawtransaction`; alle seit 0.17 stabil. Regtest gegen 0.18 und 0.21 testen. |
| Sender-Regel | Falscher erster Input → Tx gilt als von einer anderen Adresse. | Builder setzt die Input-Reihenfolge fest, Dry-Run prüft den erkannten Sender. |
| Rundung / Überlauf | Crowdsale- und STO-Mathe nutzt 128-Bit-Zwischenwerte. | `math/big` bzw. `uint128` mit Tests aus `uint256_extensions.h`. |
| Bugs in 0.18.1 | Referenz-Fehler gehören zum Konsens. | Nachbauen, dokumentieren, nicht „reparieren". |
| Freeze an `M…`-Adressen | `HashToAddress` kennt nur die Versionsbytes 48 (`L`) und 5 (alte `3…`-Form), nicht 50 (`M`). Ein Freeze-/Unfreeze-Payload, das wie `omni_sendfreeze` die `M`-Version einträgt, ist strukturell ungültig. | Decoder bildet das nach. `omni_sendfreeze`/`_unfreeze` und die Payload-Aufrufe schreiben für P2SH-Ziele das Versionsbyte 5 (umgesetzt in Phase 8, Dry-Run prüft es). Im Regtest gegen 0.18.1 noch bestätigen. |
| Iteration in `std::map` | Reihenfolge-abhängige Logik (STO-Verteilung, Consensus-Hash) folgt sortierten Keys. | Überall explizit sortieren. |
| `-txindex` am Node | Nicht jeder Node hat ihn. | v1 setzt ihn voraus, eigener Prevout-Cache in Phase 11. |
| Wartung OmniLite-Upstream | Falls OmniLite doch neue Features aktiviert. | Activations werden on-chain erkannt; unbekannte Features → Indexer stoppt mit klarer Meldung statt falsch weiterzurechnen. |

**Entschieden**
- Projektname: **OmniLite Standalone** (`omnilite-standalone`).
- DEx: kein automatisches Zahlen. Abschließen ist immer manuell.
- Keine eigene Wallet: Senden läuft über die Wallet des `litecoind`. Als Provider reiner Indexer.
- Absender und Empfänger nur `L…`/`M…`, neue Adressen `M…` (P2SH-P2WPKH, wie liteworlds.quest).
- Lizenz: MIT (wie OmniLite). Portierter Code behält die Copyright-Hinweise der Omni-Core-Entwickler.
- Repo: `github.com/tbuuol/omnilite-standalone`.


---

## 8. Nächste Schritte

1. Referenz-Node 0.18.1 aufsetzen und Sync starten (dauert, deshalb zuerst).
2. Phase 0 und 1 umsetzen.
3. `parseTransaction` aus `omnicore.cpp` vollständig lesen und den Decoder portieren (Phase 2).
