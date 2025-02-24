package http_requests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sirupsen/logrus"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Province 省份结构体
type Province struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Response 通用响应结构体
type Response struct {
	Code int        `json:"code"`
	Msg  string     `json:"msg"`
	Data []Province `json:"data"`
}

// Node 城市节点结构体
type Node struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	LineType string `json:"line_type"`
	Max      int    `json:"max"`
	AreaID   int    `json:"area_id"`
}

// NodeResponse 城市节点响应结构体
type NodeResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data []Node `json:"data"`
}

// Config 配置文件结构体
type Config struct {
	TradeIDs          []int       `yaml:"TradeIDs"`
	DownloadTestCount int         `yaml:"downloadTestCount"`
	DownloadURL       string      `yaml:"downloadURL"`
	TargetAddr        string      `yaml:"targetAddr"`
	WebServerPort     int         `yaml:"webServerPort"`
	WatchTradeID      []int       `yaml:"watchTradeID"`       // 添加 WatchTradeID 字段
	BaseAPIAddr       string      `yaml:"baseAPIAddr"`        // 新增基础 API 地址字段
	ErrTestNum        int         `yaml:"check_err_test_num"` // 新增 check_err_test_num 字段
	ConnectBaseURL    string      `yaml:"connect_base_url"`   // 新增 connect_base_url 字段
	ConnectOut        string      `yaml:"connect_out"`        // 新增 connect_out 字段
	DatabaseCFG       DatabaseCFG `yaml:"database"`
	Checker           Checker     `yaml:"checker"`
}

type Checker struct {
	BadLineMinSpeed  float64 `yaml:"bad_line_min_speed"`
	GoodLineMinSpeed float64 `yaml:"good_line_min_speed"`
}

type DatabaseCFG struct {
	DBType string `yaml:"db_type"`
}

// ChangeNodeResponse 变更节点请求的响应结构体
type ChangeNodeResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data []any  `json:"data"`
}

// Line 线路结构体
type Line struct {
	ID            int    `json:"id"`
	TradeName     string `json:"trade_name"`
	SSUser        string `json:"ss_user"`
	SSPass        string `json:"ss_pass"`
	EndpointAddr  string `json:"endpoint_addr"`
	OutboundIP    string `json:"outbound_ip"`
	LineType      string `json:"line_type"`
	GroupName     string `json:"group_name"`
	NodeName      string `json:"node_name"`
	ProjectName   string `json:"project_name"`
	CreateTime    string `json:"create_time"`
	ExpiredTime   string `json:"expired_time"`
	RemainingTime string `json:"remaining_time"`
}

// LineResponse 线路响应结构体
type LineResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data []Line `json:"data"`
}

// GetProvinces 获取省份列表
func GetProvinces(config *Config) ([]Province, error) {
	url := config.BaseAPIAddr + "/api/outApi/getProvinces"
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response Response
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, err
	}

	return response.Data, nil
}

// GetNodes 获取城市列表
func GetNodes(config *Config, provinceID int) ([]Node, error) {
	url := fmt.Sprintf(config.BaseAPIAddr+"/api/outApi/getNodes?line_id=22&project_id=592&province_id=%d", provinceID)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response NodeResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, err
	}

	return response.Data, nil
}

// ChangeNode 发送 POST 请求修改节点信息
func ChangeNode(config *Config, nodeID, tradeID int) error {
	url := config.BaseAPIAddr + "/api/outApi/changeNode"
	data := map[string]int{
		"node_id":  nodeID,
		"trade_id": tradeID,
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 10 * time.Second, // 设置超时时间为 10 秒
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var changeNodeResp ChangeNodeResponse
	err = json.Unmarshal(body, &changeNodeResp)
	if err != nil {
		return fmt.Errorf("failed to unmarshal response: %v", err)
	}

	if changeNodeResp.Code == 1000 || changeNodeResp.Msg == "节点变更成功." {
		//log.Printf("节点变更成功: %s", changeNodeResp.Msg)
	} else {
		return fmt.Errorf("节点变更失败: 代码 %d, 消息 %s", changeNodeResp.Code, changeNodeResp.Msg)
	}

	return nil
}

// ChangeLineIP 发送 GET 请求来更换线路 IP
func ChangeLineIP(config *Config, lineID int) error {
	url := fmt.Sprintf("%s/api/outApi/changeLineIpAddr?line_id=%d", config.BaseAPIAddr, lineID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("accept", "application/json")

	client := &http.Client{
		Timeout: 10 * time.Second, // 设置超时时间为 10 秒
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var changeIPResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data []any  `json:"data"`
	}
	err = json.Unmarshal(body, &changeIPResp)
	if err != nil {
		return fmt.Errorf("failed to unmarshal response: %v", err)
	}

	if changeIPResp.Code == 1000 || changeIPResp.Msg == "变更成功." {
		// 假设 1000 是成功的状态码，可根据实际情况修改
		log.Printf("更换 IP 成功: %s", changeIPResp.Msg)
	} else {
		return fmt.Errorf("更换 IP 失败: 代码 %d, 消息 %s", changeIPResp.Code, changeIPResp.Msg)
	}

	return nil
}

// GetLines 获取线路信息
func GetLines(config *Config) ([]Line, error) {
	url := config.BaseAPIAddr + "/api/outApi/getLine"
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"package": "http_requests",
				"error":   err,
			}).Error("http连接关闭失败")
		}
	}(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response LineResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, err
	}
	if len(response.Data) == 0 {
		return nil, fmt.Errorf("获取失败，data为空")
	}
	return response.Data, nil
}

// ReadConfig 读取配置文件
func ReadConfig() (*Config, error) {
	file, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatalf("读取配置文件出错: %v", err)
		return nil, err
	}

	var config Config
	err = yaml.Unmarshal(file, &config)
	if err != nil {
		log.Fatalf("解析配置文件出错: %v", err)
		return nil, err
	}

	return &config, nil
}
