package main

import (
	"os"
	"testing"
)

// TestSmokeLogin 不扫码，验证 getToken -> getCode -> getStatus 链路与 cookie 管理。
func TestSmokeLogin(t *testing.T) {
	c := newClient()

	uuid, err := getQRUUID(c)
	if err != nil {
		t.Fatalf("getQRUUID: %v", err)
	}
	t.Logf("uuid = %q", uuid)
	if uuid == "" {
		t.Fatal("uuid 为空")
	}

	if err := fetchQRCodePNG(c, uuid, "smoke_qr.png"); err != nil {
		t.Fatalf("fetchQRCodePNG: %v", err)
	}
	st, _ := os.Stat("smoke_qr.png")
	if st == nil {
		t.Fatal("二维码文件未生成")
	}
	t.Logf("二维码 PNG 大小 = %d 字节", st.Size())

	status, err := getQRStatus(c, uuid)
	if err != nil {
		t.Fatalf("getQRStatus: %v", err)
	}
	t.Logf("status = %q（期望为空字符串 = 等待扫码）", status)
}
