package checker

import (
	"bufio"
	"database/sql"
	"fmt"
	"monitoring_system/database"
	"monitoring_system/http_requests"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// DownloadManager 负责下载相关操作
type DownloadManager struct {
	DB              *sql.DB
	TradeID         int
	Config          *http_requests.Config
	ExitErrorMap    map[int]map[string]struct{} // 外层为 randomCityID，内层为 outboundIP
	ExitErrorMutex  *sync.Mutex
	downloadURL     string
	downloadURLLock sync.Mutex
}

// Checker 定义检查器结构体
type Checker struct {
	DB                      *sql.DB
	Config                  *http_requests.Config
	ExitErrorMap            map[int]map[string]struct{}
	ExitErrorMutex          *sync.Mutex
	ScannedIDs              map[int]time.Time // 存储扫描过的 city_id 及其过期时间
	ScannedMutex            sync.Mutex        // 保护 ScannedIDs 的互斥锁
	GoodLineCheckedIDs      map[int]time.Time // 存储已检查的 good_line id 及其过期时间
	GoodLineCheckedIDsMutex sync.Mutex        // 保护 GoodLineCheckedIDs 的互斥锁
}

// 记录日志并返回错误
func logAndReturnError(log *logrus.Entry, errMsg string, err error) error {
	log.Error(errMsg)
	return fmt.Errorf("%s: %w", errMsg, err)
}

// TestSOCKS5 执行 SOCKS5 测试
func TestSOCKS5(user, pass, endpointAddr, TargetAddr, nodeName, outboundIP string, testCount int) (float64, int64, error) {
	totalTime := int64(0)
	successCount := 0

	for i := 0; i < testCount; i++ {
		start := time.Now()
		dialer, err := proxy.SOCKS5("tcp", endpointAddr, &proxy.Auth{User: user, Password: pass}, proxy.Direct)
		if err != nil {
			continue
		}

		conn, err := dialer.Dial("tcp", TargetAddr)
		if err != nil {
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
			"targetAddr":   TargetAddr,
		}).Error(errMsg)
		return 0, 0, fmt.Errorf(errMsg)
	}

	successRate := float64(successCount) / float64(testCount) * 100
	avgResponseTime := totalTime / int64(successCount)
	logrus.WithFields(logrus.Fields{
		// "user":            user,
		// "endpointAddr":    endpointAddr,
		// "targetAddr":      TargetAddr,
		// "successRate":     successRate,
		// "avgResponseTime": avgResponseTime,
		// "NodeName":        nodeName,
		// "OutboundIP":      outboundIP,
	}).Warn("【Checker】SOCKS5测试成功")

	return successRate, avgResponseTime, nil
}

// NewChecker 创建一个新的检查器实例
func NewChecker(db *sql.DB, config *http_requests.Config, exitErrorMap map[int]map[string]struct{}, exitErrorMutex *sync.Mutex) *Checker {
	return &Checker{
		DB:                 db,
		Config:             config,
		ExitErrorMap:       exitErrorMap,
		ExitErrorMutex:     exitErrorMutex,
		ScannedIDs:         make(map[int]time.Time),
		GoodLineCheckedIDs: make(map[int]time.Time),
	}
}

// 封装重试逻辑
func retryOperation(operation func() error, maxRetries int, sleepDuration time.Duration, log *logrus.Entry, tradeID, randomCityID int, action string) error {
	retryCount := 0
	for {
		err := operation()
		if err == nil {
			log.WithFields(logrus.Fields{
				"TradeID":      tradeID,
				"RandomCityID": randomCityID,
				"retryCount":   retryCount,
			}).Warnf("【Checker】%s 成功", action)
			return nil
		} else {
			log.WithFields(logrus.Fields{
				"TradeID":      tradeID,
				"RandomCityID": randomCityID,
				"retryCount":   retryCount,
			}).Error("【Checker】获取线路信息失败:", "重试中", retryCount, "次")
		}
		retryCount++
		log.WithFields(logrus.Fields{
			"TradeID":      tradeID,
			"RandomCityID": randomCityID,
			"retryCount":   retryCount,
		}).Warnf("【Checker】%s 获取线路信息尝试失败，重试", action)
		if retryCount >= maxRetries {
			return fmt.Errorf("%s 重试 %d 次后失败", action, retryCount)
		}
		time.Sleep(sleepDuration)
	}
}

// Check 执行检测逻辑
func (c *Checker) Check() {
	c.ExitErrorMutex.Lock()
	defer func() {
		c.ExitErrorMutex.Unlock()
		// logrus.Warn("【Checker】已释放互斥锁")
	}()
	consecutiveFailures := 0
	if len(c.ExitErrorMap) == 0 {
		logrus.Warning("【Checker】检测ExitErrorMap为空")
		logrus.Warning("【Checker】已分配watchTradeID：", c.Config.WatchTradeID)
		for {
			var randomCityID int
			// 查询bad_line表
			err := c.DB.QueryRow("SELECT randomCityID FROM bad_line ORDER BY RANDOM() LIMIT 1").Scan(&randomCityID)
			if err != nil {
				if err == sql.ErrNoRows {
					logrus.Warn("【Checker】数据库中bad_line表没有扫描到需要检测的节点")
					consecutiveFailures++
					if consecutiveFailures >= 3 {
						// 尝试从good_line表获取数据
						var goodLineRandomCityID int
						err := c.DB.QueryRow("SELECT node_id FROM good_line ORDER BY RANDOM() LIMIT 1").Scan(&goodLineRandomCityID)
						if err != nil {
							if err == sql.ErrNoRows {
								logrus.Warn("【Checker】数据库中good_line表也没有扫描到需要检测的节点")
								logrus.Warn("【Checker】 等待 10 分钟后重新查询是否有新的记录")
								for i := 0; i < 10; i++ {
									time.Sleep(1 * time.Minute)
									logrus.Warn("【Checker】等待", 10-i, "分钟后重新查询是否有新的记录")
								}
								consecutiveFailures = 0
								break
							} else {
								logAndReturnError(logrus.WithFields(logrus.Fields{"Error": err}), "【Checker】查询 good_line 表时出错", err)
								break
							}
						}
						c.GoodLineCheckedIDsMutex.Lock()
						if expiration, exists := c.GoodLineCheckedIDs[goodLineRandomCityID]; exists && time.Since(expiration) < 30*time.Minute {
							c.GoodLineCheckedIDsMutex.Unlock()
							logrus.WithFields(logrus.Fields{"randomCityID": goodLineRandomCityID}).Warn("【Checker】good_line 中的节点 ID 已检查过，跳过检测")
							continue
						}
						c.GoodLineCheckedIDs[goodLineRandomCityID] = time.Now().Add(30 * time.Minute)
						c.GoodLineCheckedIDsMutex.Unlock()
						randomCityID = goodLineRandomCityID
					} else {
						continue
					}
				} else {
					logAndReturnError(logrus.WithFields(logrus.Fields{"Error": err}), "【Checker】查询 bad_line 表时出错", err)
					break
				}
			}
			// consecutiveFailures = 0
			if randomCityID == 0 {
				// logrus.WithFields(logrus.Fields{"randomCityID": randomCityID}).Warn("【Checker】获取的 randomCityID 为 0，跳过此次检测")
				continue
			}
			c.ScannedMutex.Lock()
			if expiration, exists := c.ScannedIDs[randomCityID]; exists && time.Since(expiration) < 30*time.Minute {
				c.ScannedMutex.Unlock()
				logrus.WithFields(logrus.Fields{"randomCityID": randomCityID}).Warn("【Checker】检测记录已存在,15秒后继续找下一个")
				time.Sleep(15 * time.Second)
				continue
			}
			c.ScannedIDs[randomCityID] = time.Now().Add(30 * time.Minute)
			c.ScannedMutex.Unlock()
			logrus.WithFields(logrus.Fields{"randomCityID": randomCityID}).Warn("【Checker】从表中获取到节点 ID：", randomCityID)
			c.processRandomCityID(randomCityID)
			break
		}
	} else {
		logrus.Error("【Checker】发现异常开始执行检测流程...")
		for randomCityID := range c.ExitErrorMap {
			if randomCityID == 0 {
				logrus.WithFields(logrus.Fields{"randomCityID": randomCityID}).Warn("【Checker】获取的 randomCityID 为 0，跳过此次检测")
				delete(c.ExitErrorMap, randomCityID)
				continue
			}
			logrus.WithFields(logrus.Fields{"randomCityID": randomCityID}).Warn("【Checker】从 ExitErrorMap 中移除节点 ID：", randomCityID)
			delete(c.ExitErrorMap, randomCityID)
			logrus.WithFields(logrus.Fields{"randomCityID": randomCityID}).Warn("【Checker】开始处理节点 ID：", randomCityID)
			c.processRandomCityID(randomCityID)
			break
		}
	}
}

// processRandomCityID 处理单个 randomCityID 的检测流程
func (c *Checker) processRandomCityID(randomCityID int) {
	// 判断是否从 good_line 表获取的 randomCityID
	isFromGoodLine := false
	var dummy int
	err := c.DB.QueryRow("SELECT 1 FROM good_line WHERE node_id =?", randomCityID).Scan(&dummy)
	if err == nil {
		isFromGoodLine = true
	}

	if randomCityID == 0 {
		logrus.WithFields(logrus.Fields{"randomCityID": randomCityID}).Warn("【Checker】传入的 randomCityID 为 0，不执行检测流程并标记为已检测")
		c.ScannedMutex.Lock()
		c.ScannedIDs[randomCityID] = time.Now().Add(30 * time.Minute)
		c.ScannedMutex.Unlock()
		return
	}
	watchTradeID := c.Config.WatchTradeID[0]
	logrus.WithFields(logrus.Fields{"randomCityID": randomCityID, "watchTradeID": watchTradeID}).Warnf("【Checker】开始处理节点 ID：%d，WorKer：%d", randomCityID, watchTradeID)

	// 更换节点到指定城市
	err = retryOperation(func() error {
		return http_requests.ChangeNode(c.Config, randomCityID, watchTradeID)
	}, 3, 1*time.Second, logrus.WithFields(logrus.Fields{"TradeID": watchTradeID, "RandomCityID": randomCityID}), watchTradeID, randomCityID, "更换节点到指定城市")
	if err != nil {
		logrus.WithFields(logrus.Fields{"TradeID": watchTradeID, "RandomCityID": randomCityID, "Error": err}).Error("【Checker】更换节点到指定城市失败")
		return
	}

	// 获取节点信息
	var lines []http_requests.Line
	err = retryOperation(func() error {
		var err error
		lines, err = http_requests.GetLines(c.Config)
		return err
	}, 3, 1*time.Second, logrus.WithFields(logrus.Fields{"TradeID": watchTradeID, "RandomCityID": randomCityID}), watchTradeID, randomCityID, "获取线路信息")
	if err != nil {
		logrus.WithFields(logrus.Fields{"TradeID": watchTradeID, "RandomCityID": randomCityID, "Error": err}).Error("【Checker】获取线路信息失败")
		return
	}

	var matchedLines []http_requests.Line
	for _, line := range lines {
		if line.SSUser == strconv.Itoa(watchTradeID) {
			matchedLines = append(matchedLines, line)
		}
	}

	totalDownloadSpeed := 0.0
	testCount := 0
	allBelow10Mbps := true

	for _, line := range matchedLines {
		logrus.WithFields(logrus.Fields{
			"randomCityID": randomCityID,
			"watchTradeID": watchTradeID,
			"NodeName":     line.NodeName,
		}).Warn("【开始处理当前线路的下载测试】")

		// 定义 errorCount 和 badOutboundIPs，确保作用域覆盖后续使用
		errorCount := 0
		badOutboundIPs := make(map[string]struct{})

		targetAddr := strings.TrimPrefix(c.Config.ConnectBaseURL, "http://")
		logrus.SetLevel(logrus.InfoLevel)

		for i := 0; i < c.Config.Errtestnum; i++ {
			successRate, avgResponseTime, err := TestSOCKS5(line.SSUser, line.SSPass, line.EndpointAddr, targetAddr, line.NodeName, line.OutboundIP, 1)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID":      watchTradeID,
					"RandomCityID": randomCityID,
					"NodeName":     line.NodeName,
					"endpointAddr": line.EndpointAddr,
					"targetAddr":   targetAddr,
					"Error":        err,
					"outboundIP":   line.OutboundIP,
				}).Error("【Checker】对节点进行 SOCKS5 测试失败")
			} else {
				logrus.WithFields(logrus.Fields{
					"TradeID":      watchTradeID,
					"RandomCityID": randomCityID,
					"NodeName":     line.NodeName,
					"SuccessRate":  successRate,
					"ResponseTime": avgResponseTime,
				}).Warning("【Checker】节点 SOCKS5 测试成功")
			}

			downloadManager := &DownloadManager{
				DB:             c.DB,
				TradeID:        watchTradeID,
				Config:         c.Config,
				ExitErrorMap:   c.ExitErrorMap,
				ExitErrorMutex: c.ExitErrorMutex,
			}
			speed, err := downloadManager.PerformDownloadTests(line, randomCityID)
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode := exitErr.ExitCode()
					if exitCode == 18 || exitCode == 28 || exitCode == 97 {
						errorCount++
						badOutboundIPs[line.OutboundIP] = struct{}{} // 记录出现错误的 outboundIP
						logrus.WithFields(logrus.Fields{
							"TradeID":      watchTradeID,
							"RandomCityID": randomCityID,
							"NodeName":     line.NodeName,
							"ExitCode":     exitCode,
							"Error":        err,
						}).Error("【Checker】下载测试遇到特定错误码，判定失败")
						// 执行 changeLineIpAddr
						err := http_requests.ChangeLineIP(c.Config, watchTradeID)
						time.Sleep(5 * time.Second)
						if err != nil {
							logrus.WithFields(logrus.Fields{
								"TradeID":      watchTradeID,
								"RandomCityID": randomCityID,
								"Error":        err,
							}).Error("【Checker】执行更换IP时出错")
						} else {
							logrus.WithFields(logrus.Fields{
								"TradeID":      watchTradeID,
								"RandomCityID": randomCityID,
							}).Warning("【Checker】更换节点 IP 成功")
						}
						continue
					}
				}
				errorCount++

				if speed > 3 {
					logrus.WithFields(logrus.Fields{
						"TradeID":      watchTradeID,
						"RandomCityID": randomCityID,
						"NodeName":     line.NodeName,
						"Error":        err,
					}).Error("【Checker】下载速率不达标,开始更换节点 IP")
				} else {
					logrus.WithFields(logrus.Fields{
						"TradeID":      watchTradeID,
						"RandomCityID": randomCityID,
						"NodeName":     line.NodeName,
						"Error":        err,
					}).Error("【Checker】下载测试失败,开始更换节点 IP")
				}
				err := http_requests.ChangeLineIP(c.Config, watchTradeID)
				time.Sleep(5 * time.Second)
				if err != nil {
					logrus.WithFields(logrus.Fields{
						"TradeID":      watchTradeID,
						"RandomCityID": randomCityID,
						"Error":        err,
					}).Error("【Checker】执行更换IP时出错")
				} else {
					logrus.WithFields(logrus.Fields{
						"TradeID":      watchTradeID,
						"RandomCityID": randomCityID,
					}).Warning("【Checker】更换节点 IP 成功")
				}

			} else {
				// 根据来源判断是否更换 IP
				if isFromGoodLine && speed < 10 {
					err := http_requests.ChangeLineIP(c.Config, watchTradeID)
					time.Sleep(5 * time.Second)
					if err != nil {
						logrus.WithFields(logrus.Fields{
							"TradeID":      watchTradeID,
							"RandomCityID": randomCityID,
							"Error":        err,
						}).Error("【Checker】执行更换IP时出错（good_line 单次速率小于10）")
					} else {
						logrus.WithFields(logrus.Fields{
							"TradeID":      watchTradeID,
							"RandomCityID": randomCityID,
						}).Warning("【Checker】更换节点 IP 成功（good_line 单次速率小于10）")
					}
				} else if !isFromGoodLine && speed < 3 {
					err := http_requests.ChangeLineIP(c.Config, watchTradeID)
					time.Sleep(5 * time.Second)
					if err != nil {
						logrus.WithFields(logrus.Fields{
							"TradeID":      watchTradeID,
							"RandomCityID": randomCityID,
							"Error":        err,
						}).Error("【Checker】执行更换IP时出错（bad_line 单次速率小于3）")
					} else {
						logrus.WithFields(logrus.Fields{
							"TradeID":      watchTradeID,
							"RandomCityID": randomCityID,
						}).Warning("【Checker】更换节点 IP 成功（bad_line 单次速率小于3）")
					}
				}
			}

			if isFromGoodLine && speed >= 10 {
				allBelow10Mbps = false
			}

			totalDownloadSpeed += speed
			testCount++
		}

		// 计算平均下载速率
		avgDownloadSpeed := 0.0
		if testCount > 0 {
			avgDownloadSpeed = totalDownloadSpeed / float64(testCount)
		}

		// 格式化平均下载速率
		downloadManager := &DownloadManager{
			DB:             c.DB,
			TradeID:        watchTradeID,
			Config:         c.Config,
			ExitErrorMap:   c.ExitErrorMap,
			ExitErrorMutex: c.ExitErrorMutex,
		}
		formattedSpeed, err := downloadManager.FormatSpeed(avgDownloadSpeed)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"TradeID":      watchTradeID,
				"RandomCityID": randomCityID,
				"Error":        err,
			}).Error("【Checker】格式化平均下载速率时出错")
			formattedSpeed = 0 // 出错时使用默认值 0
		}

		// 如果 randomCityID 来自 good_line 且所有下载测试速率都小于 10Mbps，则从 good_line 中删除
		if isFromGoodLine && allBelow10Mbps {
			_, err := c.DB.Exec("DELETE FROM good_line WHERE node_id =?", randomCityID)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID":      watchTradeID,
					"RandomCityID": randomCityID,
					"Error":        err,
				}).Error("【Checker】从 good_line 表中删除记录时出错")
			} else {
				logrus.WithFields(logrus.Fields{
					"TradeID":      watchTradeID,
					"RandomCityID": randomCityID,
				}).Warn("【Checker】从 good_line 表中删除记录成功，所有下载速率均小于 10Mbps")
			}
		}

		// 如果 randomCityID 不是来自 good_line，则执行原有的 bad_line 处理逻辑
		if !isFromGoodLine {
			if errorCount <= 2 || formattedSpeed < 3 {
				// 记录要从 bad_line 表中删除记录的日志
				logrus.WithFields(logrus.Fields{
					"TradeID":      watchTradeID,
					"RandomCityID": randomCityID,
					"NodeName":     line.NodeName,
				}).Warningf("【Checker】从 bad_line 表中删除 %s: %d", line.NodeName, randomCityID)
				// 从 bad_line 表中删除记录
				delErr := database.DeleteFromBadLine_id(c.DB, randomCityID)
				if delErr != nil {
					logrus.WithFields(logrus.Fields{
						"TradeID":      watchTradeID,
						"RandomCityID": randomCityID,
						"NodeName":     line.NodeName,
						"Error":        delErr,
					}).Error("【Checker】从 bad_line 表中删除记录时出错")
				}

				// 记录要更新 good_count 为 0 的日志
				logrus.WithFields(logrus.Fields{
					"TradeID":      watchTradeID,
					"RandomCityID": randomCityID,
					"NodeName":     line.NodeName,
				}).Warningf("【Checker】更新 %s: %d 的 good_count 为 0", line.NodeName, randomCityID)

				// 更新 good_count 为 0
				updateErr := database.UpdateGoodCount(c.DB, randomCityID, false)
				if updateErr != nil {
					logrus.WithFields(logrus.Fields{
						"TradeID":      watchTradeID,
						"RandomCityID": randomCityID,
						"OutboundIP":   line.OutboundIP,
						"Error":        updateErr,
					}).Error("【Checker】更新 good_count 时出错")
				}

				// 如果更换 IP 前有错误，且最后一次下载成功，将错误 IP 写入 bad_ips 表
				if len(badOutboundIPs) > 0 {
					exists, err := database.CheckNodeIDExistsInBadLine_id(c.DB, randomCityID)
					if err != nil {
						logrus.WithFields(logrus.Fields{
							"TradeID":      watchTradeID,
							"RandomCityID": randomCityID,
							"Error":        err,
						}).Error("【Checker】检查 randomCityID 是否存在于 bad_line 表时出错")
					} else if !exists {
						for outboundIP := range badOutboundIPs {
							err := database.InsertIntoBadIPs(c.DB, outboundIP, randomCityID)
							if err != nil {
								logrus.WithFields(logrus.Fields{
									"TradeID":      watchTradeID,
									"RandomCityID": randomCityID,
									"OutboundIP":   outboundIP,
									"Error":        err,
								}).Error("【Checker】插入记录到 bad_ips 表时出错")
							} else {
								logrus.WithFields(logrus.Fields{
									"TradeID":      watchTradeID,
									"RandomCityID": randomCityID,
									"OutboundIP":   outboundIP,
								}).Warn("【Checker】成功插入记录到 bad_ips 表")
							}
						}
					}
				}
				// 清空 badOutboundIPs 映射
				badOutboundIPs = make(map[string]struct{})
			} else {
				// 检查该 randomCityID 是否在 bad_line 中存在
				exists, err := database.CheckNodeIDExistsInBadLine_id(c.DB, randomCityID)
				if err != nil {
					logrus.WithFields(logrus.Fields{
						"TradeID":      watchTradeID,
						"RandomCityID": randomCityID,
						"Error":        err,
					}).Error("【Checker】检查 randomCityID 是否存在于 bad_line 表时出错")
				} else if !exists {
					err := database.InsertIntoBadLine(c.DB, line.OutboundIP, randomCityID)
					if err != nil {
						logrus.WithFields(logrus.Fields{
							"TradeID":      watchTradeID,
							"RandomCityID": randomCityID,
							"OutboundIP":   line.OutboundIP,
							"Error":        err,
						}).Error("【Checker】插入记录到 bad_line 表时出错")
					} else {
						logrus.WithFields(logrus.Fields{
							"TradeID":      watchTradeID,
							"RandomCityID": randomCityID,
							"OutboundIP":   line.OutboundIP,
						}).Warn("【Checker】成功插入记录到 bad_line 表")
					}
				} else {
					logrus.WithFields(logrus.Fields{
						"TradeID":      watchTradeID,
						"RandomCityID": randomCityID,
						"OutboundIP":   line.OutboundIP,
					}).Infof("【Checker】city_id 为 %d 的记录已存在", randomCityID)
				}
			}
		}
	}
}

// GetDownloadURL 获取下载 URL
func (dm *DownloadManager) GetDownloadURL() (string, error) {
	dm.downloadURLLock.Lock()
	defer dm.downloadURLLock.Unlock()
	if dm.downloadURL != "" {
		return dm.downloadURL, nil
	}
	downloadURL, err := database.GetDownloadURL(dm.DB)
	if err != nil {
		logrus.WithFields(logrus.Fields{"TradeID": dm.TradeID, "Error": err}).Error("获取下载 URL 出错")
		return dm.Config.DownloadURL, err
	}
	if downloadURL == "" {
		dm.downloadURL = dm.Config.DownloadURL
	} else {
		dm.downloadURL = downloadURL
	}
	return dm.downloadURL, nil
}

// PerformDownloadTests 进行多次下载测试以计算平均下载速率
func (dm *DownloadManager) PerformDownloadTests(line http_requests.Line, randomCityID int) (float64, error) {
	downloadURL, err := dm.GetDownloadURL()
	if err != nil {
		return 0, err
	}

	proxyURL := fmt.Sprintf("socks5://%s:%s@%s", line.SSUser, line.SSPass, line.EndpointAddr)
	downloadTestCount := dm.Config.Errtestnum
	var totalSpeed float64

	for i := 0; i < downloadTestCount; i++ {
		logrus.WithFields(logrus.Fields{
			"TradeID":      dm.TradeID,
			"randomCityID": randomCityID,
			"Test":         i + 1,
			"outboundIP":   line.OutboundIP,
			"NodeName":     line.NodeName,
		}).Warn("【Checker】开始下载测试")
		speed, err := dm.executeCurlCommand(downloadURL, proxyURL, randomCityID, line.OutboundIP)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"TradeID": dm.TradeID,
				"Error":   err,
			}).Error("【Checker】使用 curl 下载文件出错，开始更换 IP")
			// 更换 IP
			err := http_requests.ChangeLineIP(dm.Config, dm.TradeID)
			time.Sleep(5 * time.Second)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID": dm.TradeID,
					"Error":   err,
				}).Error("【Checker】执行更换 IP 时出错")
			} else {
				logrus.WithFields(logrus.Fields{
					"TradeID": dm.TradeID,
				}).Warn("【Checker】更换 IP 成功")
			}
		} else {
			logrus.WithFields(logrus.Fields{
				"TradeID":      dm.TradeID,
				"randomCityID": randomCityID,
				"Test":         i + 1,
				"outboundIP":   line.OutboundIP,
				"NodeName":     line.NodeName,
				// speed 取两位
				"Speed": fmt.Sprintf("%.2f", speed),
			}).Warn("【Checker】", randomCityID, "第", i+1, "次下载测试结果")
			totalSpeed += speed
		}
	}

	if downloadTestCount > 0 {
		avgDownloadSpeed := totalSpeed / float64(downloadTestCount)
		formattedSpeed, err := dm.FormatSpeed(avgDownloadSpeed)
		if err != nil {
			return 0, err
		}
		logrus.WithFields(logrus.Fields{
			"TradeID":      dm.TradeID,
			"randomCityID": randomCityID,
			"outboundIP":   line.OutboundIP,
			"NodeName":     line.NodeName,
			"AvgSpeed":     formattedSpeed,
		}).Warn("【Checker】", downloadTestCount, "次平均下载速率")
		return formattedSpeed, nil
	}
	return 0, nil
}

// executeCurlCommand 执行 curl 命令
func (dm *DownloadManager) executeCurlCommand(url, proxy string, randomCityID int, outboundIP string) (float64, error) {
	timestamp := time.Now().UnixNano()
	tempFileName := fmt.Sprintf("/tmp/curl_speed_output_tradeid_%d_%d", dm.TradeID, timestamp)
	outputFile, err := os.Create(tempFileName)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("创建临时文件出错")
		return 0, fmt.Errorf("创建临时文件出错: %v", err)
	}
	outputFilePath := outputFile.Name()
	defer os.Remove(outputFilePath)
	defer outputFile.Close()

	cmd := exec.Command("curl", "-x", proxy, "--insecure", "--silent", "-m 120", "--write-out", "%{speed_download}", "--output", "/dev/null", url)
	cmd.Stdout = outputFile

	if err := cmd.Start(); err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("启动 curl 命令出错")
		return 0, fmt.Errorf("启动 curl 命令出错: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			logrus.WithFields(logrus.Fields{
				"TradeID":      dm.TradeID,
				"ExitCode":     exitCode,
				"RandomCityID": randomCityID,
				"OutboundIP":   outboundIP,
			}).Warn("【Checker】curl 命令执行出错，捕获到退出码")
			if exitCode == 18 || exitCode == 28 || exitCode == 97 {
				return 0, fmt.Errorf("curl 命令执行出错，退出码: %d", exitCode)
			}
		}
		logrus.WithFields(logrus.Fields{
			"TradeID":      dm.TradeID,
			"Error":        err,
			"randomCityID": randomCityID,
		}).Error("【Checker】", randomCityID, "执行 curl 命令出错")
		return 0, fmt.Errorf("执行 curl 命令出错: %v", err)
	}

	file, err := os.Open(outputFilePath)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("打开临时文件读取内容出错")
		return 0, fmt.Errorf("打开临时文件读取内容出错: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var speedStr string
	for scanner.Scan() {
		speedStr = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("读取临时文件内容出错")
		return 0, fmt.Errorf("读取临时文件内容出错: %v", err)
	}

	speed, err := strconv.ParseFloat(strings.TrimSpace(speedStr), 64)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("解析下载速率出错")
		return 0, fmt.Errorf("解析下载速率出错: %v", err)
	}

	mbpsSpeed := speed * 8 / (1024 * 1024)
	return mbpsSpeed, nil
}

// FormatSpeed 格式化平均下载速率，保留两位小数
func (dm *DownloadManager) FormatSpeed(speed float64) (float64, error) {
	formattedSpeedStr := fmt.Sprintf("%.2f", speed)
	formattedSpeed, err := strconv.ParseFloat(formattedSpeedStr, 64)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("格式化下载速率时出错")
		return 0, err
	}
	return formattedSpeed, nil
}
