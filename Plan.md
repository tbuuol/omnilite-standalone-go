# OmniLite Standalone – Plan

**OmniLite Standalone** ist ein eigenständiger OmniLite-Indexer, der **nicht** an eine bestimmte Litecoin-Core-Version gekoppelt ist.
Er verbindet sich per Standard-RPC mit einem beliebigen, aktuellen `litecoind`, liest die Blöcke,
dekodiert Omni-Transaktionen selbst, berechnet den kompletten Omni-State (Balances, Properties,
Crowdsales, DEx, NFTs, Freeze, Activations) und stellt ihn über eine API bereit.

Darauf aufbauend ist er eine **vollwertige OmniLite-Wallet**: Er kann Token erstellen, senden und
verwalten und am DEx handeln. Er baut die Transaktionen selbst, signiert sie und gibt sie über
`sendrawtransaction` an den Node.

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
- Wallet: alle Omni-Aktionen, die `omnilitecored` kann (Send, Issuance, Grant/Revoke, Crowdsale,
  STO, Freeze, NFT, DEx Sell/Accept/Pay), mit eigener Schlüsselverwaltung. Die Litecoin-Wallet des
  Nodes wird dafür nicht gebraucht.
- Keine verbrannten Gebühren: Jede Omni-Tx wird vor dem Senden gegen den eigenen State geprüft.

**Nicht-Ziele (vorerst)**
- Keine eigene P2P-Anbindung (nur RPC).
- Keine allgemeine Litecoin-Wallet. LTC wird nur verwaltet, soweit Omni-Txs es brauchen
  (Gebühren, Dust-Outputs, DEx-Zahlungen).
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
        │  getrawtransaction, getrawmempool,                   ▲
        │  estimatesmartfee, sendrawtransaction                │ Public API (lesen)
        ▼                                                      │ Wallet API (privat, Auth)
 ┌───────────────┐   ┌─────────────┐   ┌──────────────┐   ┌────┴─────┐
 │ Block Fetcher │──▶│ Tx Decoder  │──▶│ State Engine │──▶│   API    │
 │ + Mempool     │   │ Class A/B/C │   │ (Konsens)    │   └────┬─────┘
 │ + Reorgs      │   │ Sender/Ref  │   │ Tally, SP,   │        │
 └───────▲───────┘   │ Payload     │   │ DEx, STO,    │   ┌────▼─────────────┐
         │           └─────────────┘   │ NFT ...      │◀──│ Wallet           │
         │                             └──────┬───────┘   │ Keystore, UTXOs, │
         │       sendrawtransaction           │           │ Tx-Builder,      │
         └────────────────────────────────────┼───────────│ Dry-Run, Signer  │
                                              ▼           └──────────────────┘
                                       SQLite (eingebettet)
                                       State + Tx-Liste + Undo-Journal + Wallet
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

### Wallet-Teil

Der Wallet-Teil sitzt auf der State Engine. Er weiß damit immer genau, welche Token eine Adresse hat
und welche davon in DEx-Offers oder Accepts reserviert sind.

**Schlüssel**
- Eigene HD-Wallet mit BIP39-Seed. **Neue Adressen sind ausschließlich `M…`** (BIP49 P2SH-P2WPKH,
  Pfad `m/49'/2'/0'/0/i`). Das ist der Standard-Adresstyp von OmniLite 0.18.1 (`DEFAULT_ADDRESS_TYPE =
  P2SH_SEGWIT`) und genau das, was die LiteWorlds-Wallet erzeugt (`blockchain.js`). Derselbe Seed ergibt
  also dieselben Adressen wie auf liteworlds.quest.
- Kein Bech32 (OmniLite akzeptiert das nicht als Sender). `L…` (P2PKH) wird **nicht** neu erzeugt.
- Import einzelner WIF-Keys. Auch alte `L…`-Adressen lassen sich importieren, damit Token darauf nicht
  festsitzen. Ziel ist, sie auf `M…` umzuziehen.
- Reine Watch-only-Adressen.
- Das Lesen bleibt davon unberührt: Der Indexer verarbeitet natürlich alle Adresstypen, die OmniLite kennt.
- Keystore verschlüsselt (Argon2id + AES-256-GCM), Entsperren mit Passphrase, automatisches Sperren.
- Austauschbare Signer:
  1. interner Keystore (Standard),
  2. PSBT-Export und -Import für externe Wallets oder Hardware-Wallets, der Indexer baut dann nur,
  3. optional die Wallet des `litecoind` (`signrawtransactionwithwallet`).

**UTXOs**
- Der Indexer sieht ohnehin jeden Block und den Mempool. Er führt die UTXOs der eigenen Adressen
  selbst, ein Address-Index am Node ist nicht nötig.
- Beim Import einer Adresse, die schon benutzt wurde: Startbestand über `scantxoutset` (Standard-RPC),
  ab dann laufend aus den Blöcken.
- Coin-Control: bestimmte UTXOs sperren (z. B. für laufende DEx-Zahlungen).

**Tx-Builder** (Port von `createpayload.cpp`, `encoding.cpp`, `wallettxbuilder.cpp`)
1. Payload bauen (rein, ohne Node).
2. Encoding wählen: Class C (OP_RETURN mit `omni`-Marker), wenn Marker und Payload in die
   Datacarrier-Grenze passen (80 Byte). Sonst Class B (verschleiertes Bare-Multisig plus Exodus-Output).
3. Inputs wählen. **Der erste Input muss vom Sender kommen** (Class-C-Regel). Bei Class B muss der
   Sender die größte Input-Summe stellen, deshalb nur Inputs der Sender-Adresse.
4. Outputs: Referenz-Output (Dust) an den Empfänger, falls der Typ einen braucht, Change an den Sender
   **nach** dem Referenz-Output (wegen der Change-Regel bei der Referenz-Bestimmung).
5. Gebühr über `estimatesmartfee`, Dust-Grenzen wie in Litecoin Core.
6. **Dry-Run:** Die fertige Tx läuft durch den eigenen Decoder und die State Engine (auf einer Kopie
   des aktuellen States inklusive Pending). Ist sie Omni-ungültig (zu wenig Token, falsche Property,
   Crowdsale geschlossen …), wird **nicht** gesendet. Die Chain würde die LTC-Gebühr trotzdem nehmen.
7. Signieren, `sendrawtransaction`, als Pending speichern.

**Pending**
- Eigene unbestätigte Txs zählen sofort gegen Token- und UTXO-Bestand. Sonst baut man zwei Sends,
  die zusammen mehr ausgeben, als da ist.
- Bei Reorg oder Ablauf aus dem Mempool wird die Tx wieder Pending oder als verworfen markiert.
- RBF und CPFP für hängende Txs (später).

**Zugriff**
- Zwei getrennte Schnittstellen: die **Public API** (nur lesen, darf ins Netz) und die **Wallet API**
  (bauen, signieren, senden). Die Wallet API lauscht standardmäßig nur auf `127.0.0.1`, mit
  Token-Auth, und lässt sich komplett abschalten (`wallet=0`, reiner Indexer).
- Die Website spricht nur mit der Public API. Signiert wird nie auf dem Webserver.

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
| `createpayload.cpp`, `rpcpayload.cpp` | `wallet/payload` | Payloads für alle Tx-Typen |
| `encoding.cpp`, `script.cpp` | `wallet/encode` | Class B (Multisig + Verschleierung) und Class C (OP_RETURN) |
| `wallettxbuilder.cpp`, `createtx.cpp` | `wallet/builder` | Input-Auswahl, Referenz, Change, Gebühr |
| `rpctx.cpp` | `api/wallet` | `omni_send*` |
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

### Phase 7 – Wallet-Basis und Senden
- Keystore (HD, Import, Watch-only, Verschlüsselung), UTXO-Tracking, Signer-Schnittstelle, PSBT.
- Tx-Builder mit Class C und Class B, Dry-Run, Broadcast.
- Erste Aktionen: `omni_send`, `omni_sendall`, `omni_sendnonfungible`, `omni_sendanydata`.
- Zusätzlich rohe Bausteine: `omni_createpayload_*`, `omni_createrawtx_*`, `omni_sendrawtx`.
- **Fertig, wenn:** im Regtest gebaute Txs vom Referenz-Node (`omnilitecored`) als gültig gewertet werden,
  mit identischem Ergebnis in `omni_gettransaction`, und ein absichtlich ungültiger Send vom Dry-Run
  abgefangen wird.

### Phase 8 – Token-Verwaltung
- Property erstellen: `omni_sendissuancefixed`, `omni_sendissuancemanaged`, `omni_sendissuancecrowdsale`.
- Verwalten: `omni_sendgrant`, `omni_sendrevoke`, `omni_sendclosecrowdsale`, `omni_sendchangeissuer`,
  `omni_sendenablefreezing`, `omni_senddisablefreezing`, `omni_sendfreeze`, `omni_sendunfreeze`.
- Verteilen: `omni_sendsto`.
- NFTs: Ausgabe über Managed Issuance, Grant mit NFT-Daten, NFT-Daten setzen (Typ 201).
- **Fertig, wenn:** jede Aktion im Regtest einmal durchgespielt und gegen den Referenz-Node geprüft ist.

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
- Backups: Seed-Export, verschlüsseltes Wallet-Backup.
- Metriken: Höhe, Lag zum Node, Reorgs, Dauer pro Block.

### Phase 11 – Später (optional)
- Eigener Prevout-Cache statt `-txindex`.
- RBF und CPFP für hängende Txs.
- Einfache Web-Oberfläche für die Wallet (lokal).
- Hardware-Wallet direkt anbinden statt nur über PSBT.

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
| Adresstypen | Nur P2PKH/P2SH als Sender. Wer Token an `ltc1…` schickt, verliert sie praktisch. | Wallet erzeugt nur `M…` und lehnt Bech32-Empfänger ab. P2SH-P2WPKH als Sender ist im Mainnet erprobt (LiteWorlds nutzt es), trotzdem in die Regtest-Gegenprobe aufnehmen. |
| Verlorene Gebühren | Omni-ungültige Txs landen trotzdem in der Chain. | Pflicht-Dry-Run vor jedem Broadcast, Pending zählt mit. |
| DEx-Zahlung | Zu spät, zu wenig oder an den falschen Seller gezahlt → LTC weg. | Zahlungs-Wächter, Betrag und Window im Dry-Run prüfen, nur über `omni_senddexpay` zahlen. |
| Schlüssel | Diebstahl, wenn die Wallet API offen im Netz hängt. | Wallet API nur lokal, Token-Auth, verschlüsselter Keystore, abschaltbar. |
| Sender-Regel | Falscher erster Input → Tx gilt als von einer anderen Adresse. | Builder setzt die Input-Reihenfolge fest, Dry-Run prüft den erkannten Sender. |
| Rundung / Überlauf | Crowdsale- und STO-Mathe nutzt 128-Bit-Zwischenwerte. | `math/big` bzw. `uint128` mit Tests aus `uint256_extensions.h`. |
| Bugs in 0.18.1 | Referenz-Fehler gehören zum Konsens. | Nachbauen, dokumentieren, nicht „reparieren". |
| Freeze an `M…`-Adressen | `HashToAddress` kennt nur die Versionsbytes 48 (`L`) und 5 (alte `3…`-Form), nicht 50 (`M`). Ein Freeze-/Unfreeze-Payload, das wie `omni_sendfreeze` die `M`-Version einträgt, ist strukturell ungültig. | Decoder bildet das nach. Wallet (Phase 8) schreibt für P2SH-Ziele das Versionsbyte 5, sonst geht die Gebühr verloren. Vorher im Regtest gegen 0.18.1 bestätigen. |
| Iteration in `std::map` | Reihenfolge-abhängige Logik (STO-Verteilung, Consensus-Hash) folgt sortierten Keys. | Überall explizit sortieren. |
| `-txindex` am Node | Nicht jeder Node hat ihn. | v1 setzt ihn voraus, eigener Prevout-Cache in Phase 11. |
| Wartung OmniLite-Upstream | Falls OmniLite doch neue Features aktiviert. | Activations werden on-chain erkannt; unbekannte Features → Indexer stoppt mit klarer Meldung statt falsch weiterzurechnen. |

**Entschieden**
- Projektname: **OmniLite Standalone** (`omnilite-standalone`).
- DEx: kein automatisches Zahlen. Abschließen ist immer manuell.
- Neue Wallet-Adressen nur `M…` (BIP49, wie liteworlds.quest).
- Lizenz: MIT (wie OmniLite). Portierter Code behält die Copyright-Hinweise der Omni-Core-Entwickler.
- Repo: `github.com/tbuuol/omnilite-standalone`.


---

## 8. Nächste Schritte

1. Referenz-Node 0.18.1 aufsetzen und Sync starten (dauert, deshalb zuerst).
2. Phase 0 und 1 umsetzen.
3. `parseTransaction` aus `omnicore.cpp` vollständig lesen und den Decoder portieren (Phase 2).
