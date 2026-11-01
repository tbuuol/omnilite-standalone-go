# OmniLite Indexer – Plan

Ein eigenständiger OmniLite-Indexer, der **nicht** an eine bestimmte Litecoin-Core-Version gekoppelt ist.
Er verbindet sich per Standard-RPC mit einem beliebigen, aktuellen `litecoind`, liest die Blöcke,
dekodiert Omni-Transaktionen selbst, berechnet den kompletten Omni-State (Balances, Properties,
Crowdsales, DEx, NFTs, Freeze, Activations) und stellt ihn über eine API bereit.

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

**Nicht-Ziele (vorerst)**
- Keine Wallet, kein Signieren, kein Broadcast von Transaktionen.
- Keine Pending-/Mempool-Logik in v1 (kommt ggf. später, siehe Phase 7).
- Keine eigene P2P-Anbindung (nur RPC).
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
        │  getrawtransaction, getblockchaininfo                ▲
        ▼                                                      │ JSON-RPC (omni_*) / REST
 ┌───────────────┐   ┌─────────────┐   ┌──────────────┐   ┌────┴─────┐
 │ Block Fetcher │──▶│ Tx Decoder  │──▶│ State Engine │──▶│   API    │
 │ + Reorg-      │   │ Class A/B/C │   │ (Konsens)    │   └──────────┘
 │   Erkennung   │   │ Sender/Ref  │   │ Tally, SP,   │
 └───────────────┘   │ Payload     │   │ DEx, STO,    │
                     └─────────────┘   │ NFT ...      │
                                       └──────┬───────┘
                                              ▼
                                       SQLite (eingebettet)
                                       State + Tx-Liste + Undo-Journal
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
| `test/*.cpp` | `*_test.go` | Unit-Tests portieren |

Grundsatz: **Portieren, nicht neu erfinden.** Jede Funktion bekommt im Kommentar die Quelldatei und
Zeile in OmniLite 0.18.1, aus der sie stammt.

---

## 5. Phasen

### Phase 0 – Grundgerüst
- Go-Modul, Config (RPC-URL, User/Pass, DB-Pfad, Netzwerk), Logging.
- RPC-Client für `litecoind`, Prüfung auf `-txindex`.
- **Fertig, wenn:** `omnilite-indexer --check` meldet Node-Version, Höhe und txindex-Status.

### Phase 1 – Block-Pipeline und Reorgs
- Blöcke ab `GENESIS_BLOCK` lesen, Höhe und Hash in der DB speichern, Tip folgen (Polling oder ZMQ).
- Reorg-Erkennung über `previousblockhash`, Rollback über das Undo-Journal (zunächst mit leerem State).
- **Fertig, wenn:** Sync bis zum Tip läuft und ein simulierter Reorg im Regtest sauber zurückrollt.

### Phase 2 – Decoder
- Kandidatenerkennung (Exodus-Output, Class-C-Marker), Class A/B/C, Sender und Referenz, Payload.
- Ausgabe: Liste aller Omni-Kandidaten mit Typ/Version/Rohpayload.
- **Fertig, wenn:** die Kandidatenliste für den gesamten Mainnet-Bereich exakt der Liste aus
  `omni_listblocktransactions` des Referenz-Nodes entspricht (gleiche Txids, gleiche Sender).

### Phase 3 – Basis-State
- Simple Send, Send All, Restricted Send, Issuance (fixed/managed), Grant/Revoke, Change Issuer,
  Activations/Deactivations/Alerts, Any Data.
- Gültig/Ungültig mit identischen Fehlercodes speichern.
- **Fertig, wenn:** Balances und Properties bis zum Tip mit dem Referenz-Node übereinstimmen
  (für Blöcke, in denen nur diese Typen vorkommen).

### Phase 4 – Komplexe Logik
- Crowdsales (inkl. Early Bonus, Issuer-Anteil, Deadline, Close), STO (v0 und v1),
  DEx (Offer, Accept, Zahlung, Expiry; mit FREEDEX für beliebige Properties), Freeze/Unfreeze, NFTs.
- **Fertig, wenn:** der Consensus-Hash an **jeder** Blockhöhe ab Genesis mit der Referenz identisch ist.

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

### Phase 6 – Betrieb
- Windows-Dienst und systemd-Unit, Docker-Image.
- Snapshot-Export und -Import (DB-Datei + Höhe + Hash), damit niemand ab Genesis syncen muss.
- Metriken: Höhe, Lag zum Node, Reorgs, Dauer pro Block.

### Phase 7 – Später (optional)
- Mempool und Pending-Txs (`omni_listpendingtransactions`) über `getrawmempool` und ZMQ.
- Eigener Prevout-Cache statt `-txindex`.
- Raw-Tx-Builder (`omni_createrawtx_*`) für Wallets, ohne Signieren.

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
6. CI: Unit-Tests bei jedem Push, Vollvergleich gegen den Referenz-Dump als manueller Job.

---

## 7. Risiken und offene Fragen

| Thema | Risiko | Umgang |
|---|---|---|
| MWEB / HogEx (Litecoin 0.21) | 0.18-Nodes sehen MWEB-Daten nicht. Txs, die Peg-out-Outputs der HogEx ausgeben, könnten anders erscheinen. | Bei der Kandidatenerkennung alles ignorieren, was 0.18 nicht sieht. Gezielter Test mit Peg-out-Inputs. |
| Adresstypen | Bech32 und P2SH als Sender/Referenz: was akzeptiert 0.18.1 genau? (`IsAllowedInputType`, `IsAllowedOutputType`) | 1:1 aus `rules.cpp` übernehmen, inklusive Blockhöhen. |
| Rundung / Überlauf | Crowdsale- und STO-Mathe nutzt 128-Bit-Zwischenwerte. | `math/big` bzw. `uint128` mit Tests aus `uint256_extensions.h`. |
| Bugs in 0.18.1 | Referenz-Fehler gehören zum Konsens. | Nachbauen, dokumentieren, nicht „reparieren". |
| Iteration in `std::map` | Reihenfolge-abhängige Logik (STO-Verteilung, Consensus-Hash) folgt sortierten Keys. | Überall explizit sortieren. |
| `-txindex` am Node | Nicht jeder Node hat ihn. | v1 setzt ihn voraus, eigener Prevout-Cache in Phase 7. |
| Wartung OmniLite-Upstream | Falls OmniLite doch neue Features aktiviert. | Activations werden on-chain erkannt; unbekannte Features → Indexer stoppt mit klarer Meldung statt falsch weiterzurechnen. |

**Offene Entscheidungen**
- Name des Projekts/Binaries (Arbeitstitel `omnilite-indexer`).
- Lizenz (OmniLite ist MIT; Portierung als MIT naheliegend).
- GitHub-Remote: eigenes Repo oder unter einer Org.

---

## 8. Nächste Schritte

1. Referenz-Node 0.18.1 aufsetzen und Sync starten (dauert, deshalb zuerst).
2. Phase 0 und 1 umsetzen.
3. `parseTransaction` aus `omnicore.cpp` vollständig lesen und den Decoder portieren (Phase 2).
