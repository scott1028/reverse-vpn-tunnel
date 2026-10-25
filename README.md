# reverse-vpn-tunnel
繁體中文 | [English](README.en.md)

讓 entry node（流量進入隧道的機器）經 exit node（替你連上網際網路的機器）上網，使用 [WireGuard 加密隧道](docs/guide.md#glossary)。

像在你家和朋友家之間挖一條專用走道：你出門上網都走朋友家的大門，所以網站看到的是朋友家的地址。
走道從朋友那頭挖過來，因為只有你家有找得到的門牌（public IP）。朋友那頭沒回應時，你就改走自己家的門。

```text
entry node (--server) --send--> exit node (--client) --reach--> internet
entry node (--server) <--dial-- exit node (--client)
```

兩端都需要 root，程式會修改路由與防火牆；斷線後會恢復本地路由，不會封鎖上網。

## 你需要準備什麼

- 兩台 Linux 或 macOS 機器，以及 root／sudo 權限。
- 從原始碼建置需要 Go 1.26.8 與 make；[系統工具需求](docs/guide.md#requirements)依 OS 而異。
- entry node 需要可連入的 UDP 連接埠，預設 `51820`；經過路由器時要設定 port forwarding（連接埠轉送）。
- Docker 只用於 [e2e 測試](docs/guide.md#e2e)，日常執行不需要。

## 先把兩端跑起來

你在兩端各自建置，然後從放設定檔的目錄啟動對應角色。

```sh
make release
cd dist
sudo ./reverse-vpn-tunnel --server  # entry node; use --client on the exit node
```

entry node 首次執行會建立 `reverse-vpn-tunnel.server.jsonc` 並繼續啟動；未指定金鑰時會產生並印出 `api_key`。
exit node 首次執行會建立 `reverse-vpn-tunnel.client.jsonc` 後退出；填入相同金鑰與 entry node 位址，再執行一次。
你也可以先準備下方設定檔；完整流程見[首次啟動](docs/guide.md#setup)。

## 怎麼確認流量走對了

兩端連上後，你在 entry node 執行這個命令，應看到 exit node 網路的對外 IPv4 位址。

```sh
curl -4 https://ifconfig.me
```

entry node 的 log 會出現以下訊息；時間、等級與附加欄位依執行狀況而異。

```text
server listening
peer connected
forwarding all traffic through the exit node
```


## 哪台跑哪個參數

你依「誰需要換出口」選角色，角色名稱不代表誰主動連線。

| 機器 | 參數 | 網路需求 | 設定檔 | 做什麼 |
|---|---|---|---|---|
| entry node | `--server` | 可連入的 UDP 連接埠 | `reverse-vpn-tunnel.server.jsonc` | 連線正常時，將這台的上網流量送往 exit node |
| exit node | `--client` | 能主動連到 entry node；可在 NAT 後方 | `reverse-vpn-tunnel.client.jsonc` | 連入 entry node，替它轉送流量上網 |

NAT 是網路位址轉換，讓內網機器共用對外位址；[術語表](docs/guide.md#glossary)列出其他用語。

## 設定檔最少要填什麼

你在各端的執行目錄放對應設定檔，未填欄位會沿用預設值。

entry node：`reverse-vpn-tunnel.server.jsonc`

```json
{
  "api_key": "replace-with-the-same-random-secret",
  "listen_port": 51820
}
```

exit node：`reverse-vpn-tunnel.client.jsonc`

```json
{
  "api_key": "replace-with-the-same-random-secret",
  "server_address": "server.example.com:51820"
}
```

三件事一定要記得：

- 把範例金鑰換成自己的隨機金鑰，兩端必須相同；位址也要換成可連入的 entry node 位址。
- 預設啟用 [LAN alias](docs/guide.md#lan-alias)，用另一段位址連到 exit node 內網；`auto` 偵測失敗時，指定內網網段或停用。
- entry node 的 `forward_all_traffic`、`block_ipv6` 可存檔套用；其他欄位與 exit node 設定需要重啟。

## 怎麼保護金鑰與流量

你的 `api_key` 能推導兩端私鑰，請把它當成可控制這組連線的秘密。

- 不要提交設定檔或分享首次啟動輸出；程式建立的設定檔權限為 `0600`。
- `--api-key` 會留在 shell history，也可能出現在程序列表；平常從設定檔讀取即可。
- `block_ipv6` 預設開啟，但阻擋失敗只會記錄警告；斷線會走本地出口，詳見 [IPv6 與限制](docs/guide.md#ipv6)。

## 接下來去哪裡查

[使用指南](docs/guide.md)把你後續會用到的細節分開說明。

- [CLI 與完整設定](docs/guide.md#flags)：預設值、合法範圍、存檔套用與重啟規則。
- [連線與系統變更](docs/guide.md#reconnect)：逾時、LAN alias、IPv6、停止與當機復原。
- [建置與測試](docs/guide.md#build)：Makefile、Docker e2e、log 位置與已知限制。

## 可以依什麼授權使用

這個 repository 目前沒有 `LICENSE` 檔案，也尚未聲明授權條款。
