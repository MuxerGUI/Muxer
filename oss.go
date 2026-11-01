package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	qwenOverrideKey       string
	qwenOverrideOSSID     string
	qwenOverrideOSSSecret string
	qwenOverrideBucket    string
	qwenOverrideEndpoint  string
)

func dashscopeKey() string {
	if v := strings.TrimSpace(keys().DashScopeKey); v != "" {
		return v
	}
	return strings.TrimSpace(qwenOverrideKey)
}

func ossAccessKeyID() string {
	if v := strings.TrimSpace(keys().OSSAccessKeyID); v != "" {
		return v
	}
	return strings.TrimSpace(qwenOverrideOSSID)
}

func ossAccessKeySecret() string {
	if v := strings.TrimSpace(keys().OSSAccessKeySecret); v != "" {
		return v
	}
	return strings.TrimSpace(qwenOverrideOSSSecret)
}

const (
	ossEndpointDefault = "oss-cn-shanghai.aliyuncs.com"
	ossBucketDefault   = "muxer-asr"
)

func ossBucketName() string {
	if v := strings.TrimSpace(keys().OSSBucket); v != "" {
		return v
	}
	if v := strings.TrimSpace(qwenOverrideBucket); v != "" {
		return v
	}
	return ossBucketDefault
}

func ossEndpointHost() string {
	if v := strings.TrimSpace(keys().OSSEndpoint); v != "" {
		return v
	}
	if v := strings.TrimSpace(qwenOverrideEndpoint); v != "" {
		return v
	}
	return ossEndpointDefault
}

func ossV1Sign(method, contentType, date, resource string) string {
	ak := ossAccessKeyID()
	sk := ossAccessKeySecret()
	stringToSign := method + "\n" + "\n" + contentType + "\n" + date + "\n" + resource
	mac := hmac.New(sha1.New, []byte(sk))
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return "OSS " + ak + ":" + sig
}

func ossUploadFile(localPath, objectKey string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("获取文件信息失败: %w", err)
	}

	putURL := fmt.Sprintf("https://%s.%s/%s", ossBucketName(), ossEndpointHost(), objectKey)
	date := time.Now().UTC().Format(http.TimeFormat)
	contentType := "audio/mpeg"
	resource := "/" + ossBucketName() + "/" + objectKey
	auth := ossV1Sign("PUT", contentType, date, resource)

	req, err := http.NewRequest("PUT", putURL, f)
	if err != nil {
		return err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", auth)
	req.ContentLength = fi.Size()

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("上传 OSS 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OSS 上传返回 %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func ossSignedURL(objectKey string, expireSec int64) string {
	ak := ossAccessKeyID()
	sk := ossAccessKeySecret()
	expires := time.Now().Unix() + expireSec
	resource := "/" + ossBucketName() + "/" + objectKey
	stringToSign := fmt.Sprintf("GET\n\n\n%d\n%s", expires, resource)
	mac := hmac.New(sha1.New, []byte(sk))
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("https://%s.%s/%s?OSSAccessKeyId=%s&Expires=%d&Signature=%s",
		ossBucketName(), ossEndpointHost(), objectKey,
		url.QueryEscape(ak), expires, url.QueryEscape(sig))
}

func ossDeleteObject(objectKey string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		delURL := fmt.Sprintf("https://%s.%s/%s", ossBucketName(), ossEndpointHost(), objectKey)
		date := time.Now().UTC().Format(http.TimeFormat)
		resource := "/" + ossBucketName() + "/" + objectKey
		auth := ossV1Sign("DELETE", "", date, resource)

		req, err := http.NewRequest("DELETE", delURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Date", date)
		req.Header.Set("Authorization", auth)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code == 204 || code == 200 || code == 404 {
			return nil
		}
		lastErr = fmt.Errorf("OSS 返回 %d", code)
	}
	return lastErr
}
