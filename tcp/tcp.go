package tcp

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// 自定义 logrus 格式化器
type CustomFormatter struct {
	logrus.TextFormatter
}

// Format 自定义格式化方法
func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	if strings.Contains(entry.Message, "【TCP_SERVER_MOD】接受连接") {
		// 设置为黄色
		entry.Level = logrus.WarnLevel
	}
	return f.TextFormatter.Format(entry)
}

// GetLocalIP 通过执行 curl 命令访问 ip.sb 获取本地服务器 IP 地址
func GetLocalIP() (string, error) {
	// 构建 curl 命令
	cmd := exec.Command("curl", "http://ip.sb")

	// 获取命令的输出管道
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err,
		}).Error("获取 curl 命令输出管道出错")
		return "", fmt.Errorf("获取 curl 命令输出管道出错: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err,
		}).Error("启动 curl 命令出错")
		return "", fmt.Errorf("启动 curl 命令出错: %w", err)
	}

	// 读取命令输出
	body, err := io.ReadAll(stdout)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err,
		}).Error("读取 curl 命令输出出错")
		return "", fmt.Errorf("读取 curl 命令输出出错: %w", err)
	}

	// 等待命令执行完成
	if err := cmd.Wait(); err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err,
		}).Error("执行 curl 命令出错")
		return "", fmt.Errorf("执行 curl 命令出错: %w", err)
	}

	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		logrus.WithFields(logrus.Fields{
			"ip": ip,
		}).Error("从 ip.sb 响应中获取的不是有效的 IP 地址")
		return "", fmt.Errorf("从 ip.sb 响应中获取的不是有效的 IP 地址: %s", ip)
	}

	logrus.WithFields(logrus.Fields{
		"ip": ip,
	}).Info("成功获取本地 IP 地址")

	return ip, nil
}

// ListenTCP 监听本地 TCP 端口
func ListenTCP() (string, error) {
	// 设置自定义格式化器
	logrus.SetFormatter(&CustomFormatter{
		TextFormatter: logrus.TextFormatter{
			ForceColors:     true,
			DisableColors:   false,
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
		},
	})

	// 读取配置文件
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	if err := viper.ReadInConfig(); err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err,
		}).Error("读取配置文件出错")
		return "", fmt.Errorf("读取配置文件出错: %w", err)
	}

	// 获取监听端口
	port := viper.GetString("tcpport")
	if port == "" {
		logrus.WithFields(logrus.Fields{
			"configKey": "tcpport",
		}).Error("配置文件中未设置监听端口")
		return "", fmt.Errorf("配置文件中未设置监听端口")
	}

	// 获取半连接超时时间，默认为 5 秒
	timeout := viper.GetDuration("tcp_half_connection_timeout")
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	// 监听本地 TCP 端口
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"port":  port,
			"error": err,
		}).Error("监听端口出错")
		return "", fmt.Errorf("监听端口 %s 出错: %w", port, err)
	}
	logrus.WithFields(logrus.Fields{
		"port": port,
	}).Info("【TCP_SERVER_MOD】正在监听端口")

	// 获取本地 IP 地址
	ip, err := GetLocalIP()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err,
		}).Error("获取本地 IP 地址出错")
		return "", fmt.Errorf("获取本地 IP 地址出错: %w", err)
	}

	// 用于记录已经打印过的 IP 地址及其添加时间
	printedIPs := make(map[string]time.Time)
	var mutex sync.Mutex

	// 启动一个 goroutine 定时清理过期的 IP 地址
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mutex.Lock()
			for ip, addedTime := range printedIPs {
				if time.Since(addedTime) > 5*time.Minute {
					delete(printedIPs, ip)
				}
			}
			mutex.Unlock()
		}
	}()

	// 简单的处理连接逻辑，这里只是打印连接信息
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
					logrus.WithFields(logrus.Fields{
						"error": err,
					}).Warn("接受连接时出现临时错误，等待 1 秒后重试")
					time.Sleep(1 * time.Second)
					continue
				}
				logrus.WithFields(logrus.Fields{
					"error": err,
				}).Error("接受连接出错，停止接受新连接")
				return
			}
			remoteAddr := conn.RemoteAddr().String()
			ip := strings.Split(remoteAddr, ":")[0]

			mutex.Lock()
			// 检查 IP 地址是否已经打印过或者是否过期
			if addedTime, exists := printedIPs[ip]; !exists || time.Since(addedTime) > 5*time.Minute {
				fields := logrus.Fields{
					"	remoteAddr": remoteAddr,
				}

				logrus.WithFields(fields).Info("【TCP_SERVER_MOD】接受连接")
				printedIPs[ip] = time.Now()
			}
			mutex.Unlock()

			// 处理每个连接的 goroutine
			go handleConnection(conn, timeout)
		}
	}()

	return fmt.Sprintf("%s:%s", ip, port), nil
}

// handleConnection 处理单个连接
func handleConnection(conn net.Conn, timeout time.Duration) {
	// 确保连接在函数结束时关闭
	defer func() {
		if err := conn.Close(); err != nil {
			logrus.WithFields(logrus.Fields{
				"remoteAddr": conn.RemoteAddr(),
				"error":      err,
			}).Error("关闭连接出错")
		}
	}()

	// 设置读取超时时间
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		logrus.WithFields(logrus.Fields{
			"remoteAddr": conn.RemoteAddr(),
			"timeout":    timeout,
			"error":      err,
		}).Error("设置读取超时时间出错")
		return
	}

	// 尝试读取数据
	buffer := make([]byte, 1024)
	_, err := conn.Read(buffer)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			logrus.WithFields(logrus.Fields{
				"remoteAddr": conn.RemoteAddr(),
			}).Info("【TCP_SERVER_MOD】半连接超时，关闭连接")
		}
		return
	}

	// 如果读取成功，重置读取超时时间（可选）
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		logrus.WithFields(logrus.Fields{
			"remoteAddr": conn.RemoteAddr(),
			"error":      err,
		}).Error("重置读取超时时间出错")
	}

}
