package cmd

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"monitoring_system/database"
	"monitoring_system/http_requests"
	"monitoring_system/socks5"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Socks5Tester 负责 SOCKS5 测试
type Socks5Tester struct{}

// TestSOCKS5 执行 SOCKS5 测试
func (s *Socks5Tester) TestSOCKS5(user, pass, endpointAddr, targetAddr, nodeName, outboundIP string, testCount int) (float64, int64, error) {
	return socks5.TestSOCKS5(user, pass, endpointAddr, targetAddr, nodeName, outboundIP, testCount)
}

// DownloadManager 负责下载相关操作
type DownloadManager struct {
	DB             *sql.DB
	TradeID        int
	Config         *http_requests.Config
	ExitErrorMap   map[int]map[string]struct{} // 外层为 randomCityID，内层为 outboundIP
	ExitErrorMutex *sync.Mutex
}

// GetDownloadURL 获取下载 URL
func (dm *DownloadManager) GetDownloadURL() (string, error) {
	downloadURL, err := database.GetDownloadURL(dm.DB)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("获取下载 URL 出错")
		return dm.Config.DownloadURL, err
	}
	if downloadURL == "" {
		return dm.Config.DownloadURL, nil
	}
	return downloadURL, nil
}

// PerformDownloadTests 进行多次下载测试以计算平均下载速率
func (dm *DownloadManager) PerformDownloadTests(line http_requests.Line, randomCityID int) (float64, error) {
	downloadURL, err := dm.GetDownloadURL()
	if err != nil {
		return 0, err
	}
	proxyURL := fmt.Sprintf("socks5://%s:%s@%s", line.SSUser, line.SSPass, line.EndpointAddr)
	downloadTestCount := dm.Config.DownloadTestCount
	var totalSpeed float64
	for i := 0; i < downloadTestCount; i++ {
		logrus.WithFields(logrus.Fields{
			"TradeID":      dm.TradeID,
			"randomCityID": randomCityID,
			"Test":         i + 1,
			"outboundIP":   line.OutboundIP,
			"NodeName":     line.NodeName,
		}).Info(dm.TradeID, "【开始第", i+1, "次下载测试】")
		speed, err := dm.executeCurlCommand(downloadURL, proxyURL, randomCityID, line.OutboundIP)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"TradeID": dm.TradeID,
				"Error":   err,
			}).Error("使用 curl 下载文件出错")
		} else {
			logrus.WithFields(logrus.Fields{
				"TradeID":      dm.TradeID,
				"randomCityID": randomCityID,
				"outboundIP":   line.OutboundIP,
				"NodeName":     line.NodeName,
				//speed取两位
				"Speed": fmt.Sprintf("%.2f", speed),
			}).Info(dm.TradeID, "【第", i+1, "次下载测试结果】")
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
		}).Info(dm.TradeID, "【平均下载速率】")
		return formattedSpeed, nil
	}
	return 0, nil
}

// eCurlCommand 执行 curl 命令
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

	cmd := exec.CommandContext(context.Background(), "curl", "-x", proxy, "--insecure", "--silent", "-m 120", "--write-out", "%{speed_download}", "--output", "/dev/null", url)
	cmd.Stdout = outputFile

	stderr, err := cmd.StderrPipe()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("获取 curl 标准错误输出管道出错")
		return 0, fmt.Errorf("获取 curl 标准错误输出管道出错: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := io.Copy(os.Stderr, stderr)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"TradeID": dm.TradeID,
				"Error":   err,
			}).Error("读取 curl 标准错误输出出错")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second) // 比 curl 的 -m 选项多 10 秒
	defer cancel()

	if err := cmd.Start(); err != nil {
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   err,
		}).Error("启动 curl 命令出错")
		return 0, fmt.Errorf("启动 curl 命令出错: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode := exitErr.ExitCode()
				logrus.WithFields(logrus.Fields{
					"TradeID":      dm.TradeID,
					"ExitCode":     exitCode,
					"RandomCityID": randomCityID,
					"OutboundIP":   outboundIP,
				}).Info("curl 命令执行出错，捕获到退出码")

				if exitCode == 18 || exitCode == 28 || exitCode == 97 {
					dm.ExitErrorMutex.Lock()
					defer dm.ExitErrorMutex.Unlock()

					if _, exists := dm.ExitErrorMap[randomCityID]; !exists {
						dm.ExitErrorMap[randomCityID] = make(map[string]struct{})
					}

					dm.ExitErrorMap[randomCityID][outboundIP] = struct{}{}
					var outboundIPs []string
					for ip := range dm.ExitErrorMap[randomCityID] {
						outboundIPs = append(outboundIPs, ip)
					}
					logrus.WithFields(logrus.Fields{
						"TradeID":      dm.TradeID,
						"ExitCode":     exitCode,
						"RandomCityID": randomCityID,
						"OutboundIPs":  outboundIPs,
					}).Info("【错误Curl_Erro_Map:】")

					logrus.WithFields(logrus.Fields{
						"TradeID":      dm.TradeID,
						"ExitCode":     exitCode,
						"RandomCityID": randomCityID,
						"OutboundIP":   outboundIP,
					}).Info("成功将错误信息存储到 ExitErrorMap")
				}
			}
			logrus.WithFields(logrus.Fields{
				"TradeID": dm.TradeID,
				"Error":   err,
			}).Error("执行 curl 命令出错")
			return 0, fmt.Errorf("执行 curl 命令出错: %v", err)
		}
	case <-ctx.Done():
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   ctx.Err(),
		}).Error("curl 命令执行超时")
		return 0, fmt.Errorf("curl 命令执行超时: %v", ctx.Err())
	}

	ctxWg, cancelWg := context.WithTimeout(context.Background(), 10*time.Second) // 等待 10 秒
	defer cancelWg()

	doneWg := make(chan struct{}, 1)
	go func() {
		wg.Wait()
		doneWg <- struct{}{}
	}()

	select {
	case <-doneWg:
		// 正常完成
	case <-ctxWg.Done():
		logrus.WithFields(logrus.Fields{
			"TradeID": dm.TradeID,
			"Error":   ctxWg.Err(),
		}).Error("等待 curl 标准错误输出读取超时")
		return 0, fmt.Errorf("等待 curl 标准错误输出读取超时: %v", ctxWg.Err())
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

// LineProcessor 负责处理 good_line 和 bad_line 表相关操作
type LineProcessor struct {
	DB      *sql.DB
	TradeID int
}

// ProcessGoodLine 处理 good_line 表记录
func (lp *LineProcessor) ProcessGoodLine(randomCityID int, avgResponseTime int64, avgDownloadSpeed float64) {
	done := make(chan struct{})
	go func() {
		if avgResponseTime > 500 || avgDownloadSpeed > 10 {
			err := database.UpdateGoodCount(lp.DB, randomCityID, true)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID": lp.TradeID,
					"CityID":  randomCityID,
					"Error":   err,
				}).Error("更新城市的 good_count 时出错")
				return
			}
			goodCount, err := database.GetGoodCount(lp.DB, randomCityID)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID": lp.TradeID,
					"CityID":  randomCityID,
					"Error":   err,
				}).Error("获取城市的 good_count 时出错")
				return
			}
			if goodCount >= 3 {
				err = database.InsertIntoGoodLine(lp.DB, randomCityID)
				if err != nil {
					logrus.WithFields(logrus.Fields{
						"TradeID": lp.TradeID,
						"CityID":  randomCityID,
						"Error":   err,
					}).Error("插入 node_id 到 good_line 表时出错")
				}
			}
		} else {
			err := database.UpdateGoodCount(lp.DB, randomCityID, false)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID": lp.TradeID,
					"CityID":  randomCityID,
					"Error":   err,
				}).Error("重置城市的 good_count 时出错")
				return
			}
			err = database.DeleteFromGoodLine(lp.DB, randomCityID)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID": lp.TradeID,
					"CityID":  randomCityID,
					"Error":   err,
				}).Error("从 good_line 表中删除 node_id 时出错")
			}
		}
		done <- struct{}{}
	}()

	select {
	case <-done:
		// 正常完成
	case <-time.After(5 * time.Second):
		logrus.WithFields(logrus.Fields{
			"TradeID": lp.TradeID,
			"CityID":  randomCityID,
			"Error":   "处理 good_line 表记录超时",
		}).Error("处理 good_line 表记录超时")
	}
}

// ProcessBadLine 处理 bad_line 表记录
func (lp *LineProcessor) ProcessBadLine(randomCityID int, avgResponseTime int64, avgDownloadSpeed float64, outboundIP string) {
	done := make(chan struct{})
	go func() {
		isBadLine := avgResponseTime > 20000 || avgDownloadSpeed < 3
		existsInBadLine, err := database.CheckNodeIDExistsInBadLine(lp.DB, outboundIP)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"TradeID":    lp.TradeID,
				"OutboundIP": outboundIP,
				"Error":      err,
			}).Error("检查 outbound_ip 是否存在于 bad_line 表时出错")
			return
		}

		if isBadLine {
			err := database.UpdateBadCount(lp.DB, randomCityID, true)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID": lp.TradeID,
					"CityID":  randomCityID,
					"Error":   err,
				}).Error("更新城市的 bad_count 时出错")
				return
			}
			badCount, err := database.GetBadCount(lp.DB, randomCityID)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID": lp.TradeID,
					"CityID":  randomCityID,
					"Error":   err,
				}).Error("获取城市的 bad_count 时出错")
				return
			}
			if badCount >= 3 && !existsInBadLine {
				err = database.InsertIntoBadLine(lp.DB, outboundIP, randomCityID)
				if err != nil {
					logrus.WithFields(logrus.Fields{
						"TradeID":    lp.TradeID,
						"OutboundIP": outboundIP,
						"CityID":     randomCityID,
						"Error":      err,
					}).Error("插入 outbound_ip 到 bad_line 表时出错")
				}
			}
		} else {
			err := database.UpdateBadCount(lp.DB, randomCityID, false)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"TradeID": lp.TradeID,
					"CityID":  randomCityID,
					"Error":   err,
				}).Error("重置城市的 bad_count 时出错")
				return
			}
			if existsInBadLine {
				err = database.DeleteFromBadLine(lp.DB, outboundIP)
				if err != nil {
					logrus.WithFields(logrus.Fields{
						"TradeID":    lp.TradeID,
						"OutboundIP": outboundIP,
						"Error":      err,
					}).Error("从 bad_line 表中删除 outbound_ip 时出错")
				}
			}
		}
		done <- struct{}{}
	}()

	select {
	case <-done:
		// 正常完成
	case <-time.After(5 * time.Second):
		logrus.WithFields(logrus.Fields{
			"TradeID":    lp.TradeID,
			"OutboundIP": outboundIP,
			"CityID":     randomCityID,
			"Error":      "处理 bad_line 表记录超时",
		}).Error("处理 bad_line 表记录超时")
	}
}
