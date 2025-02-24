package main

import (
	"database/sql"
	"github.com/sirupsen/logrus"
	"golang.org/x/exp/rand"
	"monitoring_system/cmd"
	"monitoring_system/database"
	"monitoring_system/http_requests"
	"strconv"
	"time"
)

// 检测逻辑封装到一个单独的函数中
func performChecks(db *sql.DB, tradeID int, config *http_requests.Config, sem chan struct{}, targetAddr string) {
	// 获取信号量
	sem <- struct{}{}
	defer func() {
		// 释放信号量
		<-sem
	}()

	// 获取所有城市 ID
	cityIDs, err := database.GetAllCityIDs(db)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": tradeID,
			"Error":   err,
		}).Error("获取城市 ID 出错")
		return
	}

	// 随机选择一个城市 ID
	rand.Seed(uint64(time.Now().UnixNano()))
	randomCityID := cityIDs[rand.Intn(len(cityIDs))]

	// 发送 POST 请求，添加重试机制
	maxRetries := 3
	var changeNodeErr error
	for i := 0; i < maxRetries; i++ {
		changeNodeErr = http_requests.ChangeNode(config, randomCityID, tradeID)
		if changeNodeErr == nil {
			break
		}
		if i == maxRetries-1 {
			logrus.WithFields(logrus.Fields{
				"TradeID": tradeID,
				"Retries": maxRetries,
				"Error":   changeNodeErr,
			}).Error("变更节点时出错，重试多次后仍失败")
			return
		}
		logrus.WithFields(logrus.Fields{
			"TradeID": tradeID,
			"Retry":   i + 1,
		}).Error("变更节点失败，重试中...")
		time.Sleep(2 * time.Second) // 重试间隔 2 秒
	}

	// 获取线路信息，添加重试机制
	var lines []http_requests.Line
	var getLinesErr error
	for i := 0; i < maxRetries; i++ {
		logrus.WithFields(logrus.Fields{
			"TradeID": tradeID,
			// "Retry":   i + 1,
		}).Info(tradeID, "【尝试获取线路信息...】") // 添加调试信息
		lines, getLinesErr = http_requests.GetLines(config)
		if getLinesErr == nil {
			logrus.WithFields(logrus.Fields{
				"TradeID": tradeID,
				// "Retry":   i + 1,
			}).Info(tradeID, "【成功获取线路信息】") // 添加调试信息
			break
		}
		if i == maxRetries-1 {
			logrus.WithFields(logrus.Fields{
				"TradeID": tradeID,
				"Retries": maxRetries,
				"Error":   getLinesErr,
			}).Error("【获取线路信息出错，重试多次后仍失败】")
			return
		}
		logrus.WithFields(logrus.Fields{
			"TradeID": tradeID,
			"Retry":   i + 1,
		}).Error(i, "次", "获取线路信息失败，重试中...")
		time.Sleep(2 * time.Second) // 重试间隔 2 秒
	}

	// 查找命中的线路
	var matchedLines []http_requests.Line
	for _, line := range lines {
		tradeIDStr := strconv.Itoa(tradeID)
		if line.SSUser == tradeIDStr {
			matchedLines = append(matchedLines, line)
		}
	}

	socks5Tester := &cmd.Socks5Tester{}
	downloadManager := &cmd.DownloadManager{
		DB:             db,
		TradeID:        tradeID,
		Config:         config,
		ExitErrorMap:   curlExitErrorMap,
		ExitErrorMutex: &curlExitErrorMutex,
	}
	lineProcessor := &cmd.LineProcessor{
		DB:      db,
		TradeID: tradeID,
	}

	// 对命中的线路进行处理
	for _, line := range matchedLines {
		// 进行 SOCKS5 测试
		successRate, avgResponseTime, err := socks5Tester.TestSOCKS5(line.SSUser, line.SSPass, line.EndpointAddr, targetAddr, line.NodeName, line.OutboundIP, 10)
		nodeName := removeLeadingChar(line.NodeName)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"TradeID":  tradeID,
				"NodeName": line.NodeName,
				"Error":    err,
			}).Error("【对节点进行 SOCKS5 测试出错】")
			successRate = 0
			avgResponseTime = -1
		} else {
			logrus.WithFields(logrus.Fields{
				"TradeID":      tradeID,
				"NodeName":     line.NodeName,
				"SuccessRate":  successRate,
				"ResponseTime": avgResponseTime,
				"randomCityID": randomCityID,
			}).Info("【节点SOCKS5测试结果】")
		}

		// 进行多次下载测试以计算平均下载速率
		avgDownloadSpeed, err := downloadManager.PerformDownloadTests(line, randomCityID)
		if err != nil {
			continue
		}

		// 加锁保护数据库操作
		dbMutex.Lock()
		// 保存节点检测结果到数据库，包括下载速率和节点 ID
		err = database.SaveNodeTestResult(db, nodeName, successRate, int64(avgResponseTime), avgDownloadSpeed, randomCityID)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"TradeID":  tradeID,
				"NodeName": nodeName,
				"Error":    err,
			}).Error("保存节点检测结果到数据库时出错")
		}

		// 处理 good_line 表记录
		lineProcessor.ProcessGoodLine(randomCityID, avgResponseTime, avgDownloadSpeed)

		// 处理 bad_line 表记录
		lineProcessor.ProcessBadLine(randomCityID, avgResponseTime, avgDownloadSpeed, line.OutboundIP)

		// 解锁
		dbMutex.Unlock()
	}

	logrus.WithFields(logrus.Fields{
		// "TradeID": tradeID,
	}).Info("=【", tradeID, "完成处理检测流程】 =")
}

func updateDatabase(db *sql.DB, config *http_requests.Config) {
	// 获取省份列表
	provinces, err := http_requests.GetProvinces(config)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"Error": err,
		}).Fatal("获取省份列表出错")
	}

	// 存储省份列表到数据库
	err = database.SaveProvinces(db, provinces)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"Error": err,
		}).Fatal("存储省份列表到数据库出错")
	}

	// 获取并存储城市列表
	for _, province := range provinces {
		nodes, err := http_requests.GetNodes(config, province.ID)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"Province": province.Name,
				"Error":    err,
			}).Error("获取省份节点信息出错")
			continue
		}
		err = database.SaveNodes(db, nodes)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"Province": province.Name,
				"Error":    err,
			}).Error("保存省份节点信息到数据库出错")
		}
	}
}

// removeLeadingChar 移除字符串前面的单个字符
func removeLeadingChar(s string) string {
	if len(s) > 1 {
		return s[1:]
	}
	return s
}
