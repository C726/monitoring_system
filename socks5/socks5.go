package socks5

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// TestSOCKS5 对指定的 SOCKS5 代理进行测试
func TestSOCKS5(user, pass, endpointAddr, targetAddr, nodeName, outboundIP string, testCount int) (float64, int64, error) {
	totalTime := int64(0)
	successCount := 0

	for i := 0; i < testCount; i++ {
		start := time.Now()
		dialer, err := proxy.SOCKS5("tcp", endpointAddr, &proxy.Auth{User: user, Password: pass}, proxy.Direct)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"user":         user,
				"endpointAddr": endpointAddr,
				"testIndex":    i,
				"error":        err,
			}).Error("创建 SOCKS5 拨号器失败")
			continue
		}

		conn, err := dialer.Dial("tcp", targetAddr)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				// "user":         user,
				// "endpointAddr": endpointAddr,
				// "targetAddr":   targetAddr,
				// "testIndex":    i,
				"error": err,
			}).Error(" SOCKS5代理测试失败")
			continue
		}
		conn.Close()

		elapsed := time.Since(start).Milliseconds()
		totalTime += elapsed
		successCount++
	}

	if successCount == 0 {
		errMsg := fmt.Sprintf("所有测试请求均失败，连接信息: %s，账号: %s，密码: %s", endpointAddr, user, pass)
		logrus.WithFields(logrus.Fields{
			"user":         user,
			"endpointAddr": endpointAddr,
			"targetAddr":   targetAddr,
		}).Error(errMsg)
		return 0, 0, fmt.Errorf(errMsg)
	}

	successRate := float64(successCount) / float64(testCount) * 100
	avgResponseTime := totalTime / int64(successCount)

	logrus.WithFields(logrus.Fields{
		// "user":            user,
		// "endpointAddr":    endpointAddr,
		// "targetAddr":      targetAddr,
		// "successRate":     successRate,
		// "avgResponseTime": avgResponseTime,
		// "NodeName":        nodeName,
		// "OutboundIP":      outboundIP,
	}).Info("【SOCKS5代理测试成功】")

	return successRate, avgResponseTime, nil
}
