// linx-device obtains an activation code without ever printing the device token.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/go-wyvern/undercover/internal/linx"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	fallback := os.Getenv("LINX_DEVICE_ID")
	if fallback == "" {
		fallback = strings.Split(os.Getenv("LINX_DEVICE_IDS"), ",")[0]
	}
	device := flag.String("device", fallback, "optional stable locally administered MAC")
	flag.Parse()
	if *device == "" {
		mac := make([]byte, 6)
		if _, err := rand.Read(mac); err != nil {
			fail(err)
		}
		mac[0] = (mac[0] | 2) & 0xfe
		*device = strings.ToUpper(net.HardwareAddr(mac).String())
		fmt.Printf("请将此身份保存到 .env，下次重试复用同一个 MAC：LINX_DEVICE_IDS=%s\n", *device)
	}
	parsed, err := net.ParseMAC(strings.TrimSpace(*device))
	if err != nil || len(parsed) != 6 {
		fail(fmt.Errorf("invalid software device MAC"))
	}
	*device = strings.ToUpper(parsed.String())
	p, err := linx.New(linx.Config{DeviceIDs: []string{*device}, OTAURL: os.Getenv("LINX_OTA_URL")})
	if err != nil {
		fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := p.OTA(ctx, *device, "undercover/"+*device)
	if err != nil {
		fail(err)
	}
	fmt.Printf("软件终端：%s\n.env 配置：LINX_DEVICE_IDS=%s\n", *device, *device)
	if out.Activation != nil {
		fmt.Printf("激活码：%s\n请在灵矽控制台的智能体 → 设备管理中绑定。绑定后启动应用即可自动获取语音凭证。\n", out.Activation.Code)
	} else if out.WebSocket.Token != "" {
		fmt.Println("软件终端已绑定，语音凭证已获取（不显示）。")
	} else {
		fail(fmt.Errorf("未收到绑定状态，请检查设备配置"))
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
