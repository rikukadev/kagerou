// Package readiness は「アプリが応答するまで ready にしない」(#26)の判定部。
// スタック完成 = 利用者の「使える」ではないので、URL に 200 が返るまで待つ。
package readiness

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout は readiness_timeout 未指定時の待ち時間。
const DefaultTimeout = 90 * time.Second

const interval = 3 * time.Second

// Wait は baseURL+path が 200 を返すまでポーリングする。返らないまま
// timeout したらエラー(呼び出し側は state を ready にしない)。
func Wait(ctx context.Context, baseURL, path string, timeout time.Duration) error {
	url := strings.TrimSuffix(baseURL, "/") + "/" + strings.TrimPrefix(path, "/")
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	var last string
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err.Error()
		} else {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = resp.Status
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s: GET %s → %s", timeout, url, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
