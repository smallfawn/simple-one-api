package handler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/sashabaranov/go-openai"
	"io"
	"log"
	"net/http"
	"simple-one-api/pkg/adapter"
	"simple-one-api/pkg/config"
	"simple-one-api/pkg/llm/devplatform/cozecn"
	"simple-one-api/pkg/utils"
	"strings"
)

var defaultCozecnURL = "https://api.coze.cn/open_api/v2/chat"
var defaultCozecomURL = "https://api.coze.com/open_api/v2/chat"

func OpenAI2CozecnHandler(c *gin.Context, s *config.ModelDetails, oaiReq openai.ChatCompletionRequest) error {
	// 使用统一的api_key获取
	secretToken := s.Credentials[config.KEYNAME_API_KEY]
	if secretToken == "" {
		secretToken = s.Credentials[config.KEYNAME_TOKEN]
	}

	cozecnReq := adapter.OpenAIRequestToCozecnRequest(oaiReq)
	cozeServerURL := s.ServerURL

	if cozeServerURL == "" {
		switch s.ServiceName {
		case "cozecn":
			cozeServerURL = defaultCozecnURL
		case "cozecom":
			cozeServerURL = defaultCozecomURL
		default:
			cozeServerURL = defaultCozecnURL
		}
	}

	log.Println(cozeServerURL)

	// 使用统一的错误处理函数
	if err := sendRequest(c, secretToken, cozeServerURL, cozecnReq, oaiReq); err != nil {
		log.Printf("处理请求失败: %v\n", err)
		return err
	}

	return nil
}

func sendRequest(c *gin.Context, token, url string, request interface{}, oaiReq openai.ChatCompletionRequest) error {
	jsonData, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("json编码错误: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return handleCozecnResponse(c, resp, oaiReq)
}

func handleCozecnResponse(c *gin.Context, resp *http.Response, oaiReq openai.ChatCompletionRequest) error {
	if oaiReq.Stream {
		return handleCozecnStreamResponse(c, oaiReq, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var respJson cozecn.Response
	if err := json.Unmarshal(body, &respJson); err != nil {
		return fmt.Errorf("json解码错误: %v", err)
	}

	if respJson.Code != 0 {
		return fmt.Errorf("错误码: %d, 错误信息: %s", respJson.Code, respJson.Msg)
	}

	myresp := adapter.CozecnReponseToOpenAIResponse(&respJson)
	myresp.Model = oaiReq.Model
	c.JSON(http.StatusOK, myresp)

	return nil
}

func handleCozecnStreamResponse(c *gin.Context, oaiReq openai.ChatCompletionRequest, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	utils.SetEventStreamHeaders(c)

	for scanner.Scan() {
		line := scanner.Text()
		//log.Println(line)
		if strings.HasPrefix(line, "data:") {
			log.Println(line)
			line = strings.TrimPrefix(line, "data:")
			var response cozecn.StreamResponse
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				log.Println(err)
				return fmt.Errorf("解析响应数据错误: %v", err)
			}
			//log.Println(response)
			switch response.Event {
			case "message":
				if response.Message.Type == "verbose" {
					continue
				}
				oaiRespStream := adapter.CozecnReponseToOpenAIResponseStream(&response)
				oaiRespStream.Model = oaiReq.Model
				respData, err := json.Marshal(&oaiRespStream)
				if err != nil {
					log.Println(err)
					return err
				}

				log.Println(string(respData))
				_, err = c.Writer.WriteString("data: " + string(respData) + "\n\n")
				if err != nil {
					log.Println(err)
				}
				c.Writer.(http.Flusher).Flush()

			case "done":

				return nil
			case "error":
				log.Printf("Chat 错误结束: %s\n", response.ErrorInformation.Msg)
				return fmt.Errorf("错误码: %d, 错误信息: %s", response.ErrorInformation.Code, response.ErrorInformation.Msg)
			default:
				fmt.Printf("未知事件: %s\n", line)
				return errors.New("message error:" + line)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取流式响应数据错误: %v", err)
	}

	return nil
}
