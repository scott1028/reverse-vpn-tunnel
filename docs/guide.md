# 把 reverse-vpn-tunnel 跑起來
繁體中文 | [English](guide.en.md)

<a id="glossary"></a>

## 先認識你會看到的用語

你可以用這張表對照設定、命令與 log，不必先熟悉 VPN 網路工具。

| 用語 | 白話解釋 |
|---|---|
| entry node | 執行 `--server` 的機器；你要改走另一個出口的流量從這裡進入隧道。 |
| exit node | 執行 `--client` 的機器；它主動連到 entry node，替 entry node 轉送流量上網。 |
| peer | 加密連線的另一端；每台機器只設定一個對端。 |
| tunnel／WireGuard | 兩端之間的加密網路連線；這裡用程式內建的 userspace WireGuard，不需要另開 `wg` 服務。 |
| LAN | 一台機器所在的內網，例如 `192.168.1.0/24`。 |
| LAN alias | 給 exit node 內網另一段存取位址，讓兩端內網重複時仍能指定遠端主機。 |
| public／private IP | public IP 是對外可路由的位址；private IP 是給內網使用的位址。 |
| DNS／A 記錄 | DNS 將名稱查成位址；A 記錄提供 IPv4 位址。 |
| root／sudo | root 是系統管理員身分；sudo 讓你用管理員身分執行命令。 |
| dead peer timeout | 多久收不到對端封包就視為斷線；設定名為 `dead_peer_timeout_sec`。 |
| 判死 | 認定對方已斷線。 |
| fail-open | 對端判死後，entry node 才恢復本地路由；判死前流量會卡在隧道，它不是斷線封網機制。 |
| state file | 程式記下原始系統值與復原資訊的檔案，讓下次啟動能處理當機遺留設定。 |
| lock file | 每個角色各自的鎖定檔，同一台機器的同一角色不能同時開兩個實例。 |
| CIDR | 用位址與斜線後的位元數表示網段，例如 `/24` 有 256 個位址；較小的位元數代表較大的網段。 |
| MTU | 單個網路封包允許的大小；`mtu` 的單位是 bytes。 |
| UDP／port forwarding | UDP 是隧道外層使用的傳輸協定；port forwarding 是路由器把外部連接埠轉送到內網機器。 |
| NAT／NETMAP／MASQUERADE | NAT 改寫封包位址；NETMAP 保留主機位元做一對一映射，MASQUERADE 使用出口介面的位址。 |
| TUN／utun | 作業系統交給程式讀寫 IP 封包的虛擬網路介面；macOS 將它命名為 `utunN`。 |
| policy routing／fwmark | policy routing 依規則挑路由表；fwmark 是 Linux 用來替封包選規則的標記。 |
| 路由／預設路由／main table | 路由決定封包送去哪裡；預設路由用於沒有更明確路由的目的地，main table 是 Linux 的主要路由表。 |
| firewall／PF anchor | firewall 是防火牆；macOS 的 PF 用 anchor 收納一組規則，這裡有專用名稱。 |
| jump／PF token | jump 是轉到另一個防火牆規則鏈的規則；PF token 是啟用 PF 後取得的參照識別碼，供清理時釋放。 |
| sysctl／rp_filter／src_valid_mark | sysctl 讀寫核心網路設定；`rp_filter` 檢查封包來源路徑，`src_valid_mark` 讓檢查考慮標記。 |
| handshake／keepalive／probe | handshake 建立加密工作階段；keepalive 維持連線；probe 是程式送出的 ICMP echo 探測封包。 |
| socket | 程式向作業系統收發網路封包的端點。 |
| mtime／stderr | mtime 是檔案最後修改時間；stderr 是程序的標準錯誤輸出，預設顯示在終端。 |
| SIGINT／SIGTERM／SIGHUP | 停止程序的訊號；`Ctrl-C` 送 SIGINT，關閉 SSH 或終端機會送 SIGHUP，本程式收到後會清理並停止。 |
| SIGKILL | 強制停止且無法攔截的訊號，程式沒有機會清理。 |
| tmux／service | tmux 保留終端工作階段；service 是交給系統長期管理的背景程序。 |
| tick／ControlPort | tick 是迴圈的一次檢查；ControlPort 是隧道內告別通知的 UDP 目的埠，entry node 不會綁定這個埠。 |
| GitHub Actions／workflow／job | GitHub Actions 執行自動化 workflow；job 是其中一組在 runner 機器執行的步驟。 |
| tag／release／asset／draft | tag 標記 Git commit；release 是 GitHub 的版本頁，asset 是下載檔，draft 是尚未公開的 release。 |
| checksum／SHA-256 | checksum 是檔案的檢查碼，這裡用 SHA-256 雜湊比對下載內容是否一致。 |
| e2e／API | e2e 是端到端測試，這裡會測真實 Linux 網路；API 是供其他程式呼叫的介面。 |

<a id="requirements"></a>

## 啟動前要準備什麼

你先備妥系統工具與連線條件，避免程式在修改網路設定時才發現缺少工具。

| 項目 | 你要準備的內容 |
|---|---|
| OS | Linux 或 macOS；程式只提供這兩種系統的網路實作，沒有聲明最低 OS 版本。 |
| 權限 | 兩端都用 root／sudo；一般使用者會收到 `must run as root (sudo)`。 |
| 建置 | Go 1.26.8 以上、make；建置會取得 `go.mod` 內的相依套件。 |
| Linux 工具 | `ip`、`iptables`、`sysctl`，以及可用的 TUN；LAN alias 還需要 iptables 的 NETMAP 支援。 |
| macOS 工具 | `ifconfig`、`route`、`pfctl`、`sysctl`；exit node 的 PF 主規則需要下述 anchor 入口。 |
| entry node 網路 | 可連入的 IPv4 UDP 連接埠，預設 `51820`；路由器與防火牆要允許這條連線。 |
| exit node 網路 | 能連到 entry node 的 IPv4 位址與連接埠；它可在 NAT 後方，毋須開放外部連入連接埠。 |
| 測試工具 | `curl` 供你檢查出口；只有 e2e 需要 Docker 與 Docker Compose。 |

兩端的 `api_key`、`tunnel_cidr` 必須相同；使用 LAN alias 時，兩端的 alias 網段也要相同。
你需要能連入的 entry node 位址；程式沒有協助穿越雙端 NAT 的中介服務。

<a id="setup"></a>

## 第一次怎麼讓兩端連起來

你先啟動 entry node 並取得金鑰，再讓 exit node 用這把金鑰連入。

```text
Time ----------------------------------------------------------------------------------------------->
you        | start entry   | run exit once    | copy key/address   | fill exit config   | rerun exit
entry node | listen        |                  | share key/address  |                    | accept <--+
exit node  |               | write template   |                    |                    | dial -----+
```

`--server` 會持續執行；`--client` 在建立範本那次會正常退出。
以下命令都從 repository 根目錄開始；兩台機器各自保留自己的設定檔。

### 在 entry node 建立設定

你在 entry node 建置並啟動，程式會在目前目錄尋找 entry node 設定。

```sh
make release
cd dist
sudo ./reverse-vpn-tunnel --server
```

找不到 `reverse-vpn-tunnel.server.jsonc` 時，程式建立完整 JSONC 範本後繼續啟動；JSONC 是允許註解的 JSON。
未帶 `--api-key` 時，它產生 32 個隨機 bytes，以無 padding 的 base64url 編碼，並將金鑰印到 stderr 一次。
你把金鑰私下交給 exit node；也可在兩端直接填入 [README 的最小設定](../README.md)。

### 在 exit node 填好設定再啟動

你在 exit node 執行一次來建立範本，填完必填欄位才會開始連線。

```sh
make release
cd dist
sudo ./reverse-vpn-tunnel --client
```

首次會印出 `Created reverse-vpn-tunnel.client.jsonc. Fill in api_key and server_address, then run again.` 後退出。
填入 entry node 的 `api_key`，把 `server_address` 換成可連入的 IPv4 或 DNS 名稱加連接埠，再執行 `sudo ./reverse-vpn-tunnel --client`。
範本預設開啟 LAN alias；`auto` 偵測不適用時，先調整[內網設定](#lan-alias)。

### 已經有設定檔時會怎樣

你重新啟動時會沿用目前目錄的設定，程式不會覆蓋既有檔案。

建立新檔使用 `0600` 權限；經 sudo 執行時，程式依 `SUDO_UID`、`SUDO_GID` 將新設定檔交還原使用者。
若你仍帶 `--api-key`，它必須與 entry node 設定檔一致；不同就退出，並提示你編輯檔案。
少填的欄位沿用預設值；未知欄位、格式錯誤或超出合法範圍會阻止啟動。

<a id="daily-use"></a>

## 平常怎麼確認它有在工作

你從放設定檔的目錄啟動，再看 log 與 entry node 的實際出口位址。

| 位置 | 你會看到的原始訊息 | 代表什麼 |
|---|---|---|
| entry node | `server listening` | 介面與監聽設定已完成，尚不表示對端已連上。 |
| exit node | `client started` | 轉送規則與介面已完成，接著會嘗試連線。 |
| entry node | `peer connected` | 對端符合存活條件。 |
| exit node | `connected to server` | 已確認 entry node 存活。 |
| entry node | `forwarding all traffic through the exit node` | 已套用全流量路由。 |
| entry node | `local routing restored` | 已撤回全流量路由；基礎隧道路由仍在。 |
| entry node | `peer dead; restoring local routing` | 對端判死，開始恢復本地路由。 |
| exit node | `server dead; redialing later` | entry node 判死，稍後會再連線。 |
| exit node | `redialing server` | 正在嘗試重新連線。 |
| entry node | `exit node said goodbye` | 告別通知讓對端從存活變成判死，接著由 `peer dead; restoring local routing` 記錄還原。 |
| entry node | `config reloaded` | 讀取成功，兩個可即時更新的欄位已載入。 |
| entry node | `config changes other than forward_all_traffic / block_ipv6 need a restart` | 有其他欄位變更，必須重啟才會套用。 |

在 entry node 執行 `curl -4 https://ifconfig.me`，連線正常且 `forward_all_traffic: true` 時，應看到 exit node 網路的對外 IPv4。
加 `--debug` 可看到唯讀系統命令與失敗資訊；修改系統的命令原本就會記錄，log 寫入 stderr，程式不另建 log 檔。

<a id="reload"></a>

## 存檔後哪些設定會立即生效

你只能在 entry node 即時切換兩個欄位，其他變更要停止程式後重啟。

| 設定變更 | 套用方式 |
|---|---|
| entry node `forward_all_traffic` | 存檔後套用；`false` 恢復本地路由，隧道與 LAN alias 路由保留。 |
| entry node `block_ipv6` | 存檔後套用；只在全流量路由啟用時阻擋 IPv6。 |
| entry node 其他欄位，含 `reconnect` | 需要重啟；即使同時修改，也只有上面兩個欄位會即時套用。 |
| exit node 所有欄位 | 需要重啟；exit node 沒有設定監看器。 |
| entry node 新設定不合法或讀取失敗 | 保留前一份設定，記錄 `config not applied; keeping previous settings`。 |

監看器每 2 秒比對一次 mtime 與大小，連續兩次相同才讀取；支援覆寫存檔與另存後 rename。
維持同一 mtime 與大小的編輯可能偵測不到；讀取失敗後要再存檔才重試，`SIGHUP` 會停止程式，不是重新載入。

<a id="reconnect"></a>

## 對端沒回應時會怎麼恢復

你正在全流量轉送時若失聯，entry node 的流量會卡在隧道，直到判定對端已死才恢復本地出口。

```text
Time -------------------------------------------------------------------------------->
[forwarding] --lose replies--> [traffic stuck] --declare peer dead--> [local routing]
```

從未連上時維持本地路由；下表的等待時間針對原本正在轉送的連線。
這是 fail-open，恢復後不會強制所有流量只能走隧道，切換時既有連線可能中斷。

### 你會等多久

你依 exit node 離線的方式估算等待時間，三種情況都要等判死後才恢復本地路由。

| 情況 | 判死與恢復時機 |
|---|---|
| exit node 正常停止 | 收到告別通知後，同一次迴圈還原路由，約 1 tick（2 秒）。 |
| 當機或斷線，entry node 仍在送資料 | 首個未回覆的資料 tick 起算約 `FastDeadAfter`（25 秒），下一個取樣確認後還原。 |
| 完全閒置 | 沿用 ping-pong：靜默 18 秒後送 probe，超過 `dead_peer_timeout_sec` 才判死；預設 45 秒、最小 40 秒。 |

tick 是每 2 秒一次的檢查；上表時間是約略值，還會受取樣與系統命令耗時影響。
告別通知若送不到，就改由未回覆資料或閒置逾時判死；不會因發送告別失敗而卡住清理。

### 正常停止怎麼通知另一端

你不用開放額外的對外連接埠，告別通知只在已加密的隧道內傳送。

| 項目 | 實際行為 |
|---|---|
| 控制通道 | entry node 在 WireGuard 解密後、寫入 TUN 前辨識通知；`ControlPort`（`51821`）只是隧道內的 UDP 目的埠，不會建立本機 listener 或占用埠。 |
| 訊息與來源 | IPv4 UDP 的 payload 必須剛好是 `rvt-bye-1`，來源是 exit node 的隧道位址（預設 `10.77.0.2`），目的地是 entry node 的隧道位址（預設 `10.77.0.1`）與上述目的埠；符合時通知迴圈並吞掉封包。 |
| 送出端防護 | exit node 在讀取 TUN 時丟棄符合告別格式的封包；只有自己開始發送告別才放行，且該次執行不再關閉放行狀態。 |
| 發送與清理 | exit node 收到 SIGINT／SIGTERM／SIGHUP 後，在關閉隧道前發送 3 次，相隔約 100 ms；整段預算 400 ms，少於 500 ms，失敗只記錄警告；送出 socket 在 Linux 用 `SO_BINDTODEVICE`、macOS 用 `IP_BOUND_IF`，讓通知從隧道送出。 |

entry node 只從 WireGuard 已驗證、解密的對端封包接受告別，exit node 則在自己開始發送告別前攔下符合格式的封包。
這兩道檢查在 Linux 與 macOS 都生效，避免任一端 LAN 主機在 exit node 正常運作時偽造告別；其他封包原樣、依序通過。
OS 與核心防火牆看不到告別通知，所以不必為這則通知新增放行規則；WireGuard 外層的 `listen_port` 仍須允許連入。

### 程式怎麼判斷存活

你看到的「連上」同時需要近期收包與成功 handshake，單靠重試封包不算。

每 2 秒取樣一次；收包計數要有變化，最後收包距今不超過 `dead_peer_timeout_sec`，handshake 距今小於 200 秒。
兩端固定設 15 秒 keepalive；閒置 ping-pong 在已存活的對端靜默 18 秒後開始送 probe，並在後續迴圈持續探測。
告別或逾時判死後，都需要比判死時間更新的 handshake 才恢復。

### 有資料卻沒回覆時怎麼加快

只有 entry node 追蹤未回覆資料，exit node 保留原本的存活迴圈。

一個 keepalive 訊息是 32 bytes；每個 tick 最多容納兩個，所以 `tx_bytes` 增量超過 64 bytes 才視為資料 tick，並扣掉本程式 probe 的傳送量。
從最後一次收包後的首個資料 tick 計時，任何收包變化都會重置；未回覆滿 `PeerReplyWithin + TickInterval`（12 秒）就送 probe，即使原本流量已停止也繼續探測。
`FastDeadAfter` 留兩段 10 秒回覆時間、兩個 2 秒 tick 與 1 秒往返餘量，共 25 秒，可容忍一次遺失的回覆；只有 keepalive 或自身閒置 probe 不會觸發快速判死。

### 兩端會怎麼重試

你可用 `reconnect` 控制重試間隔與上限，但兩端的重試工作不同。

| 角色 | 斷線與重試行為 |
|---|---|
| entry node | 曾連上後判死就恢復本地路由；每 `retry_interval_sec` 秒刷新本地 UDP socket，不能主動撥回 exit node。首次從未連上時不計這種重試。 |
| exit node | 首次即嘗試連線；失敗或判死後，按間隔重新解析 DNS、替換 peer 並建立新 handshake。 |
| 兩端 | 預設間隔 `60` 秒、`max_retries: 0` 表示持續重試；成功存活會重設次數。設定正數並用完次數後，以 `reconnect.max_retries exhausted` 結束並清理。 |

程式跟隨預設路由的出口介面；迴圈間隔大於 6 秒時視為喚醒，entry node 刷新 socket，exit node 重新連線。
exit node 每 30 秒檢查轉送規則；Linux 缺少專用 jump 時重建，macOS anchor 缺少 NAT 規則時重載，並非逐條驗證全部規則。

<a id="lan-alias"></a>

## 兩邊內網重複時怎麼連遠端主機

你用 alias 位址指定 exit node 的內網主機，就不會和 entry node 原本的同名內網位址混淆。

```text
entry node --send to 10.78.0.20--> exit node --map to 192.168.1.20--> LAN host
```

例子假設 exit node 內網為 `192.168.1.0/24`，alias 為 `10.78.0.0/16`；對應範圍是 `10.78.0.0/24`，保留主機位元。
你可在 entry node 用 `curl http://10.78.0.20` 存取遠端主機；它仍須提供 HTTP 服務並允許這條連線。

### `auto` 找不到內網時怎麼辦

你可明確指定 `lan_alias.lan_cidr`，避免程式選到不適合的介面位址。

`auto` 先找 IPv4 預設路由的介面，再取它的第一個 IPv4 網段；不是掃描所有內網，也不會跳過不合格位址再找下一個。
網段必須是 private IPv4、網路位址對齊，大小介於 `/16` 到 `/30`；public IP、過大網段、`/31`、`/32` 都會拒絕。
多網卡或多位址時，你可填入像 `192.168.1.0/24` 的實際網段，或設 `lan_alias.enabled: false` 只保留上網轉送。

### 哪些網段必須配對

你調整網段時要一起改兩端，避免送進隧道後找不到對應目標。

| 條件 | 你要檢查的事 |
|---|---|
| alias 配對 | entry node `exit_lan_alias_cidr` 等於 exit node `lan_alias.alias_cidr`；預設都是 `10.78.0.0/16`。 |
| alias 容量 | alias 網段不能小於 LAN；實際映射範圍從 alias 的起點開始，採 LAN 的前綴長度。 |
| 避免衝突 | alias 不可與 `tunnel_cidr` 重疊；啟動也會檢查 tunnel／alias 和本機網段是否衝突，Linux 另查 main table 非預設路由。 |

這是單一內網的位址映射，不提供 DNS 名稱映射，也沒有多內網設定清單；停用全流量轉送後，基礎 alias 路由仍可使用。

<a id="ipv6"></a>

## IPv6 會走哪裡

隧道承載 IPv4，所以你需要決定全流量轉送時如何處理 entry node 的 IPv6。

| 狀態 | 行為 |
|---|---|
| 全流量路由啟用，`block_ipv6: true` | 嘗試阻擋對外 IPv6；Linux 用 unreachable 路由與 policy rules，macOS 用兩條 reject 路由。 |
| 全流量路由啟用，`block_ipv6: false` | 不額外阻擋 IPv6，它可能照本地路由直接上網。 |
| 全流量路由停用或對端判死 | 撤回程式加入的 IPv6 阻擋，恢復本地路由。 |

阻擋預設開啟，但失敗只記錄 `IPv6 block not applied` 等警告，不會中止 IPv4 隧道；更明確的本地路由仍可能優先。
exit node 沒有 `block_ipv6` 欄位；entry node 的 DNS 設定不會自動改成 exit node 的 DNS。

<a id="cleanup"></a>

## 怎麼停止並保留復原能力

你用正常停止訊號讓程式撤回網路變更，只有還原成功才刪除 state file。

```text
running --receive SIGINT / SIGTERM / SIGHUP--> restore routes and rules --succeed--> remove state file
```

`Ctrl-C` 送出 SIGINT；關閉 SSH 或終端機會送 SIGHUP，程式會清理並停止，SIGPIPE 則忽略，避免 log pipe 關閉時打斷清理。
TUN 介面會隨正常關閉移除；若清理失敗，程式記錄 `cleanup incomplete; state file kept for the next start`。

### `/var/run` 內的檔案做什麼

你不必把狀態檔放進設定目錄，程式依角色固定寫在系統執行目錄。

```text
/var/run/
|-- reverse-vpn-tunnel-server.json
|-- reverse-vpn-tunnel-client.json
|-- reverse-vpn-tunnel-server.lock
+-- reverse-vpn-tunnel-client.lock
```

state file 記錄原始 sysctl 值、macOS PF token 與 utun 資訊，以 `0600` 暫存檔加 rename 儲存。
lock 依角色獨立，取得不到鎖就拒絕第二個實例；程序退出會釋放鎖，但 `.lock` 檔案可繼續存在。

### 當機後為什麼拒絕啟動

你下次啟動時，程式會先嘗試復原前次遺留變更，避免新設定蓋過還沒處理的狀態。

若 SIGKILL 或當機沒機會清理，保留 state file；無法讀取檔案時會報 `read state file`，無法完成還原則顯示 `a previous run left changes that could not be undone, so not starting`。
你依錯誤後列出的命令檢查並手動還原，再啟動一次；不要先刪 state file，否則會丟失原始系統值。
macOS 若舊 utun 已消失或不再屬於本程式，會保留無法確認歸屬的 IPv6 reject 路由並印出手動刪除命令；它不保證自動清乾淨。

<a id="flags"></a>

## CLI 每個參數怎麼用

你每次只選一個角色，參數只影響啟動方式，設定檔位置固定為目前工作目錄。

| 參數 | 預設 | 用途與限制 |
|---|---|---|
| `--server` | `false` | 跑 entry node；讀取 `reverse-vpn-tunnel.server.jsonc`，缺少時建立並繼續啟動。 |
| `--client` | `false` | 跑 exit node；讀取 `reverse-vpn-tunnel.client.jsonc`，缺少時只建立範本並退出。 |
| `--api-key KEY` | 空字串 | 只可與 `--server` 使用；至少 20 bytes，首次建立設定時採用，已有設定則比對是否相同。 |
| `--debug` | `false` | 額外記錄唯讀系統命令、它們的失敗與 debug 訊息；不改變網路策略。 |
| `--help` | — | 顯示用法與旗標，不啟動隧道；這是標準 flag 套件處理的說明入口。 |

同時選兩個角色、不選角色或多加位置參數會報 `use exactly one of --server or --client`；沒有設定檔路徑參數。
`--api-key` 會出現在程序列表與 shell history；建立設定後省略它即可。

<a id="server-config"></a>

## entry node 的完整設定有哪些

你在 `reverse-vpn-tunnel.server.jsonc` 只填要改的欄位，其餘會沿用下表預設值。

| Key | 預設值 | 合法範圍 | 白話用途 |
|---|---|---|---|
| `api_key` | 載入預設 `""`；首次建立會產生金鑰或採 `--api-key` | 至少 20 bytes；空字串不合法 | 和 exit node 共用的秘密，用來推導兩端金鑰。 |
| `listen_port` | `51820` | 整數 `1`–`65535` | entry node 接收隧道連線的 UDP 埠。 |
| `forward_all_traffic` | `true` | `true`／`false` | 對端存活時，把本機上網流量改走 exit node。 |
| `block_ipv6` | `true` | `true`／`false` | 啟用全流量路由時嘗試阻擋對外 IPv6。 |
| `tunnel_cidr` | `"10.77.0.0/30"` | 對齊的 IPv4 網段，前綴 `/0`–`/30`，不可和 alias／本機網段衝突 | 隧道內使用的網段；起始位址加一給 entry node，加二給 exit node，預設為 `10.77.0.1`、`10.77.0.2`。 |
| `exit_lan_alias_cidr` | `"10.78.0.0/16"` | 對齊的 IPv4 網段，`/0`–`/32`，不可和 tunnel／本機網段衝突 | 將這段目的位址送往 exit node，需配對它的 alias 設定。 |
| `mtu` | `1280` | 整數 `576`–`1500` | 建立 TUN 時設定的封包大小。 |
| `reconnect` | 下節的三個預設值 | 物件，欄位見 [重連設定](#reconnect-config) | 控制多久判死與怎麼重試。 |

`tunnel_cidr`、`api_key` 必須與 exit node 一致；網段合法仍可能因本機路由重疊而無法啟動。
只有 `forward_all_traffic`、`block_ipv6` 支援即時更新，其餘重啟才生效。

`home2_lan_alias_cidr` 已棄用，請改用 `exit_lan_alias_cidr`；舊鍵仍可讀取，但同時填兩個鍵會出錯。

<a id="client-config"></a>

## exit node 的完整設定有哪些

你在 `reverse-vpn-tunnel.client.jsonc` 指定 entry node 與內網映射，所有修改都要重啟才套用。

| Key | 預設值 | 合法範圍 | 白話用途 |
|---|---|---|---|
| `api_key` | `""`，範本也留空 | 至少 20 bytes；空字串不合法 | 複製 entry node 的秘密，讓兩端能建立同一組連線。 |
| `server_address` | `"server.example.com:51820"` | 非空 `host:port`，port 是 `1`–`65535` 的整數；連線時僅接受 IPv4／DNS A 記錄 | 你要連入的 entry node 位址，不是 exit node 位址。 |
| `tunnel_cidr` | `"10.77.0.0/30"` | 對齊的 IPv4 網段，`/0`–`/30`，不可和啟用的 alias／本機網段衝突 | 必須與 entry node 相同。 |
| `lan_alias` | `enabled: true`、`alias_cidr: "10.78.0.0/16"`、`lan_cidr: "auto"` | 物件 | 決定是否讓 entry node 存取你的內網。 |
| `lan_alias.enabled` | `true` | `true`／`false` | `false` 停用內網位址映射，但保留上網轉送。 |
| `lan_alias.alias_cidr` | `"10.78.0.0/16"` | 啟用時須為對齊的 IPv4 網段，`/0`–`/32`，不可和 tunnel／本機網段衝突，容量至少等於 LAN | entry node 用來連你內網主機的另一段位址。 |
| `lan_alias.lan_cidr` | `"auto"` | `"auto"` 或對齊的 private IPv4 網段 `/16`–`/30`；啟用時檢查 | 真正要映射的內網；`auto` 取預設路由介面的第一個 IPv4 網段。 |
| `mtu` | `1280` | 整數 `576`–`1500` | 建立 TUN 時設定的封包大小。 |
| `reconnect` | 下節的三個預設值 | 物件，欄位見 [重連設定](#reconnect-config) | 控制多久判死與怎麼重試。 |

DNS 每次重新連線都會再查詢，只取第一筆 IPv4 結果，查詢時限為 10 秒；不是持續監看 DNS。
停用 alias 不會略過 JSON 型別與 CIDR 語法解析，但會略過 alias 的啟用條件驗證。

<a id="reconnect-config"></a>

## 兩端的重連欄位怎麼填

你可分別調整兩端的 `reconnect`，缺少的子欄位仍會沿用預設值。

| Key | 預設值 | 合法範圍 | 白話用途 |
|---|---|---|---|
| `reconnect.dead_peer_timeout_sec` | `45` | 整數，至少 `40`，程式未另設上限 | 超過這段收包空窗就判定對端失聯，單位為秒。 |
| `reconnect.retry_interval_sec` | `60` | 整數，至少 `1`，程式未另設上限 | 等多久再嘗試一次重連，單位為秒。 |
| `reconnect.max_retries` | `0` | 整數，至少 `0`，程式未另設上限 | `0` 持續重試；正數限制斷線期間的重試次數，成功連上後歸零。 |

不要把判死逾時降到 40 秒以下；驗證會拒絕，而且它要留時間給 keepalive、probe 與新 handshake。

<a id="system-changes"></a>

## 程式會改哪些系統設定

你可用下表確認程式操作的範圍，尤其是主機已經跑其他 VPN 或 Docker 時。

| 系統與角色 | 使用工具 | 實際變更 |
|---|---|---|
| Linux 兩端 | `ip` | 建立 `rvt0` 的 IPv4 位址與路由；固定用 `rvt0`，所以同一台 Linux 不能同時跑兩個實例。 |
| Linux entry node | `ip`、`sysctl` | table `21078` 放隧道預設路由；policy rules 優先序 `5250`、`5251`、`5252`，protocol `86`，fwmark `0x5256` 讓外層 UDP 保留本地路由；`iif lo` 只選本機產生的流量，main table 更明確的路由優先。 |
| Linux entry node | `sysctl` | `net.ipv4.conf.all.src_valid_mark=1`；將 `all` 與非 lo 介面的嚴格 `rp_filter=1` 改為 `2`，記錄原值供還原。 |
| Linux entry node | `ip -6` | 需要時在 table `21078` 放 `unreachable default` 與同組 policy rules；撤回全流量設定時移除。 |
| Linux exit node | `iptables`、`sysctl` | 啟用 `net.ipv4.ip_forward=1`；建立 `RVT-FWD`（filter/FORWARD）、`RVT-POST`（nat/POSTROUTING），alias 啟用才加 `RVT-PRE`（nat/PREROUTING）；專用 jump 插在前端，使用 ACCEPT、MASQUERADE 與 NETMAP，不改既有 FORWARD policy。 |
| macOS 兩端 | `ifconfig`、`route` | 設定系統分配的 `utunN`，加入 point-to-point 位址與基礎路由。 |
| macOS entry node | `route` | 加入 `0.0.0.0/1`、`128.0.0.0/1` 到 utun；外層 IPv4 UDP socket 用 `IP_BOUND_IF` 綁實體介面；需要時加 `::/1`、`8000::/1` 經 `::1` 的 reject 路由。 |
| macOS exit node | `pfctl`、`sysctl` | 啟用 `net.inet.ip.forwarding=1`；在 `com.apple/reverse-vpn-tunnel` anchor 加 NAT 與可選的 `rdr ... bitmask`，用 `pfctl -E` 取得 token，清理時用 `pfctl -X` 釋放。 |

macOS exit node 要求主規則有 `nat-anchor "com.apple/*"` 與 `rdr-anchor "com.apple/*"`；缺少時會退出並提示還原 `/etc/pf.conf`。
Linux 清理只刪除符合本程式 selector、priority 與 protocol 的規則；相同優先序和 protocol 卻不同內容時會拒刪，table `21078` 則會整表清空。
Linux 還原 `net.ipv4.ip_forward=0` 時會提醒：若 Docker 在 exit node 執行期間啟動，你可能需要重啟 Docker。

<a id="build"></a>

## 怎麼建置與跑一般測試

你用 Makefile 建置目前主機的平台，release 與 debug binary 分開命名。

| 指令 | 輸出或用途 |
|---|---|
| `make`／`make help` | 顯示可用目標，不建置。 |
| `make release` | `dist/reverse-vpn-tunnel`；`CGO_ENABLED=0`、`-trimpath`、`-ldflags="-s -w"`，移除符號與除錯資訊。 |
| `make debug` | `dist/reverse-vpn-tunnel-debug`；`CGO_ENABLED=0`、`-gcflags="all=-N -l"`，停用最佳化與內聯，保留除錯資訊。 |
| `make test` | `go test ./...`；未命中快取時，idle 測試約需 80 秒。 |
| `make clean` | 只刪上述兩個 binary，保留 `dist/` 目錄與其中的設定檔。 |
| `go vet ./...` | 檢查 Go 程式的常見問題，不啟動隧道。 |

`dist/` 與 `.tmp/` 都在 `.gitignore`；這是避免誤提交的規則，並非保護設定檔秘密的存取控制。
Makefile 沒有跨平台目標；把 binary 搬到其他機器前，確認作業系統與 CPU 架構相符。

<a id="release"></a>

## 怎麼發布或重發同一個版本

你推送 `v*` tag 後，GitHub Actions 會先檢查程式，再產生可下載的 release 檔案。

```text
tag --trigger--> vet and test --build--> archives --replace--> release
```

測試與建置分成兩個 Ubuntu job，建置使用測試過的 commit；同一 tag 的流程依序執行，不會同時發布。
流程先依 `tag_name` 列出既有 release，包含 draft；刪除符合 tag 的 release 與舊 assets，保留 tag，再建立標題為 tag、附自動產生說明的新 release。

### 第一個版本怎麼切

你先確認要發布的 commit 已通過測試，再從那個 commit 建立 `v0.1.0` tag。

```sh
git tag v0.1.0 && git push origin v0.1.0
```

workflow 在 `.github/workflows/release.yml`，預設只有 `contents: read`；測試 checkout 不保留憑證，只有 release job 取得 `contents: write`，並用 `GITHUB_TOKEN` 呼叫 `gh` 發布。
你也可以用 `workflow_dispatch` 的 `tag` 輸入重跑既有版本；它讀取你指定的 tag。

### 同一個 tag 怎麼換成新 commit

你要保留版本名稱但替換內容時，先刪遠端與本地 tag，再把它指到要發布的 commit 並重推。

```sh
git push origin :refs/tags/v0.1.0
git tag -d v0.1.0
git tag v0.1.0
git push origin v0.1.0
```

先切到目標 commit 再執行；重推後會替換原 release，原 assets 不會保留。
同一 commit 只要重跑 workflow 即可，不必移動 tag；tag 名稱以 `v` 開頭且不含斜線。

### 下載哪個檔案

你依機器的 OS 與 CPU 架構選檔案，解開後會得到保留執行權限的 `reverse-vpn-tunnel`。

| 平台 | Asset 名稱 |
|---|---|
| Linux amd64 | `reverse-vpn-tunnel_<tag>_<shorthash>_linux_amd64.tar.gz` |
| macOS arm64 | `reverse-vpn-tunnel_<tag>_<shorthash>_darwin_arm64.tar.gz` |
| SHA-256 檢查碼 | `reverse-vpn-tunnel_<tag>_<shorthash>_checksums.txt`，包含上述兩個壓縮檔的雜湊。 |

`<shorthash>` 是 tag 所指 commit 的前 7 個字元；只提供這兩種平台，打包前使用 `CGO_ENABLED=0`、`-trimpath`、`-ldflags="-s -w"` 建置。
把你選的壓縮檔與同版本 checksums 檔放進同一個空目錄；檢查碼使用檔名，不含目錄前綴。

### 只下載 Linux 壓縮檔時怎麼驗證

你只挑 Linux 那一行檢查，就不會因為缺少另一平台檔案而失敗。

```sh
grep '_linux_amd64\.tar\.gz$' reverse-vpn-tunnel_*_checksums.txt | sha256sum -c
```

### 只下載 macOS 壓縮檔時怎麼驗證

你只挑 macOS 那一行檢查，就不會因為缺少另一平台檔案而失敗。

```sh
grep '_darwin_arm64\.tar\.gz$' reverse-vpn-tunnel_*_checksums.txt | shasum -a 256 -c
```

<a id="e2e"></a>

## 怎麼驗證真實 Linux 網路行為

你用 Docker e2e 在容器裡驗證路由、轉送與清理，避免直接修改主機的隧道設定。

```text
build binary --build image--> start six containers --run checks--> save logs --finish--> remove containers
```

它需要 Linux 主機、Go、Bash、可操作的 Docker daemon 與 Docker Compose，還需要 privileged 容器、TUN 和 macvlan 網路支援。
第一次建置要能取得 `ubuntu:24.04` 與 apt 套件；測試包含 124 秒 idle 等待與其他等待，通常要數分鐘，首次下載會更久。
退出 trap 將 log 寫到 `.tmp/e2e/server.log`、`.tmp/e2e/client.log`，binary 放在 `.tmp/e2e/bin/reverse-vpn-tunnel`。

### 你要怎麼執行測試

你在 repository 根目錄執行下列命令，主機不必用 sudo，但你的使用者必須能操作 Docker。

```sh
bash test/e2e/run.sh
```

測試使用 Compose project `rvt-e2e`，執行前後會移除該 project 的容器與 volumes；不要和另一份同名測試同時跑。
它檢查出口位址、重複內網的 alias、idle／probe、兩種存檔方式的即時更新、遺失 jump 的修復、預設拒絕 INPUT 下的正常停止 goodbye 與 6 秒內恢復、持續送流量斷線後 38 秒內恢復，以及 SIGTERM／SIGHUP 清理。
它不測 macOS；內部網路使用 `10.201.1.0/24`、`10.201.2.0/24`、`10.201.3.0/24`、`192.168.77.0/24`，請避免和現有 Docker 網路衝突。

<a id="security"></a>

## 金鑰與操作有哪些界線

你把金鑰交給對端時，也交出了推導雙方私鑰的能力，所以只和你信任的機器配對。

- `api_key` 經 Argon2id（密碼推導）與 HKDF-SHA256（金鑰展開）產生兩端私鑰與預共享金鑰；它不是登入帳號，也沒有每個使用者獨立的權限。
- 系統命令不攜帶金鑰，WireGuard 設定 log 會遮蔽 private key 與 preshared key；首次產生的 `api_key` 仍會印出，請保護終端與 log；自己建立的設定檔要執行 `chmod 600 reverse-vpn-tunnel.*.jsonc`，載入時不會自動修正權限。
- fail-open 會恢復本地上網；IPv6 阻擋失敗只記錄警告，程式也不自動更換 DNS。不要把它當作斷線封網或完整隱私隔離機制。

<a id="limitations"></a>

## 遇到問題時先查什麼

你依症狀找下一個檢查點，不必一次猜測所有網路設定。

### 連線一直建立不起來

你先確認位址與固定資源，排除不支援的連線方式。

| 症狀 | 先查什麼 |
|---|---|
| 想接第二個 exit node 或加入其他使用者 | 這裡只支援一組 peer，共享一把 `api_key`；沒有多 exit node、使用者清單或控制 API。 |
| 用 IPv6 位址連不上 | exit node 只解析 A 記錄；隧道只轉送 IPv4，IPv6 用阻擋處理。 |
| 介面、table 或規則衝突 | Linux 固定 `rvt0`、table `21078`、protocol `86`，macOS 固定 PF anchor 名稱；角色鎖不協調其他 VPN 的資源。 |

### 出口或內網位址和預期不同

你先分清楚本機流量與更明確的本地路由，再查 alias 網段。

| 症狀 | 先查什麼 |
|---|---|
| 其他 LAN 機器或 Docker 流量沒有改出口 | Linux 的 `iif lo` 只選 entry node 本機產生的流量；macOS 用主機路由，沒有相同的 Linux 選流規則。 |
| 少數目的地仍走本地 | Linux main table 非預設路由、macOS 比 `/1` 更明確的路由仍優先；全流量轉送不保證覆蓋每條路由。 |
| LAN 偵測失敗 | `auto` 不搜尋多個內網；明確設定 private IPv4 `/16`–`/30`，或停用 alias。 |

### 執行一陣子後停止或網路未恢復

你先查程序與清理訊息，再檢查防火牆是否移除了專用規則。

| 症狀 | 先查什麼 |
|---|---|
| 防火牆重載後不通 | 定期修復只檢查 jump／NAT 是否存在，不會完整稽核其他防火牆軟體或每條規則。 |
| 當機後網路未恢復 | 看 state file 與錯誤列出的清理命令；macOS 舊 utun 消失後，IPv6 reject 路由可能要手動刪除。 |
| SSH 斷開後隧道停止 | 關掉 SSH 或終端機會送 SIGHUP 並停止、清理；要長時間執行請用 `tmux` 或自備 service 設定，程式沒有內建安裝流程。 |

回到[中文 README](../README.md)可重跑最小流程；repository 目前沒有 `LICENSE`，尚未聲明授權條款。
