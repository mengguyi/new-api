package controller

import (
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type BepUsdtPayRequest struct {
	Amount int64 `json:"amount"`
}

func RequestBepUsdtAmount(c *gin.Context) {
	var req AmountRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	if req.Amount < getBepUsdtMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", getBepUsdtMinTopup())})
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getBepUsdtPayMoney(float64(req.Amount), group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

func RequestBepUsdtPay(c *gin.Context) {
	var req BepUsdtPayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	if req.Amount < getBepUsdtMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("充值数量不能小于 %d", getBepUsdtMinTopup()), "data": 10})
		return
	}

	id := c.GetInt("id")
	user, _ := model.GetUserById(id, false)
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	chargedMoney := GetChargedAmount(float64(req.Amount), *user)
	payMoney := getBepUsdtPayMoney(float64(req.Amount), group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	tradeNo := fmt.Sprintf("BEPUSDT%d%s%d", id, common.GetRandomString(6), time.Now().UnixMilli())

	callbackAddress := service.GetCallbackAddress()
	notifyUrl := strings.TrimRight(callbackAddress, "/") + "/api/bepusdt/webhook"
	redirectUrl := paymentReturnPath("/console/log")

	apiUrl := strings.TrimRight(setting.BepUsdtApiUrl, "/")
	createUrl := apiUrl + "/api/v1/order/create-order"

	params := map[string]interface{}{
		"order_id":     tradeNo,
		"amount":       payMoney,
		"notify_url":   notifyUrl,
		"redirect_url": redirectUrl,
		"fiat":         "USD",
	}
	params["signature"] = bepUsdtSign(params, setting.BepUsdtAuthToken)

	bodyBytes, err := common.Marshal(params)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	httpReq, err := http.NewRequest("POST", createUrl, strings.NewReader(string(bodyBytes)))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("BEPUSDT 创建订单请求失败 user_id=%d trade_no=%s error=%q", id, tradeNo, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("BEPUSDT 读取响应失败 user_id=%d trade_no=%s error=%q", id, tradeNo, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	var result map[string]interface{}
	if err := common.Unmarshal(respBody, &result); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("BEPUSDT 解析响应失败 user_id=%d trade_no=%s body=%q error=%q", id, tradeNo, string(respBody), err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	statusCode, _ := result["status_code"].(float64)
	if int(statusCode) != 200 {
		msg, _ := result["message"].(string)
		logger.LogError(c.Request.Context(), fmt.Sprintf("BEPUSDT 创建订单失败 user_id=%d trade_no=%s status_code=%v message=%q", id, tradeNo, statusCode, msg))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	data, _ := result["data"].(map[string]interface{})
	paymentUrl, _ := data["payment_url"].(string)
	if paymentUrl == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("BEPUSDT 响应缺少支付链接 user_id=%d trade_no=%s body=%q", id, tradeNo, string(respBody)))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	amount := req.Amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dAmount := decimal.NewFromInt(amount)
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		amount = dAmount.Div(dQuotaPerUnit).IntPart()
	}

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          amount,
		Money:           chargedMoney,
		TradeNo:         tradeNo,
		PaymentMethod:   model.PaymentMethodBepUsdt,
		PaymentProvider: model.PaymentProviderBepUsdt,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	err = topUp.Insert()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("BEPUSDT 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", id, tradeNo, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("BEPUSDT 充值订单创建成功 user_id=%d trade_no=%s amount=%d money=%.2f payment_url=%q", id, tradeNo, req.Amount, chargedMoney, paymentUrl))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"payment_url": paymentUrl,
		},
	})
}

func BepUsdtWebhook(c *gin.Context) {
	ctx := c.Request.Context()

	if !isBepUsdtWebhookEnabled() {
		logger.LogWarn(ctx, fmt.Sprintf("BEPUSDT webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("BEPUSDT webhook 读取请求体失败 path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("BEPUSDT webhook 收到请求 path=%q client_ip=%s body=%q", c.Request.RequestURI, c.ClientIP(), string(payload)))

	var notify map[string]interface{}
	if err := common.Unmarshal(payload, &notify); err != nil {
		logger.LogError(ctx, fmt.Sprintf("BEPUSDT webhook 解析请求体失败 path=%q client_ip=%s error=%q body=%q", c.Request.RequestURI, c.ClientIP(), err.Error(), string(payload)))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	receivedSig, _ := notify["signature"].(string)
	expectedSig := bepUsdtSign(notify, setting.BepUsdtAuthToken)
	if receivedSig == "" || receivedSig != expectedSig {
		logger.LogWarn(ctx, fmt.Sprintf("BEPUSDT webhook 验签失败 path=%q client_ip=%s received=%q expected=%q", c.Request.RequestURI, c.ClientIP(), receivedSig, expectedSig))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("BEPUSDT webhook 验签成功 client_ip=%s", c.ClientIP()))

	status, _ := notify["status"].(float64)
	orderId, _ := notify["order_id"].(string)
	tradeId, _ := notify["trade_id"].(string)
	actualAmount, _ := notify["actual_amount"].(string)
	token, _ := notify["token"].(string)
	blockTxId, _ := notify["block_transaction_id"].(string)

	if int(status) != 2 {
		logger.LogInfo(ctx, fmt.Sprintf("BEPUSDT webhook 订单未完成 order_id=%s status=%d client_ip=%s", orderId, int(status), c.ClientIP()))
		_, _ = c.Writer.Write([]byte("ok"))
		return
	}

	if orderId == "" {
		logger.LogWarn(ctx, fmt.Sprintf("BEPUSDT webhook 缺少订单号 client_ip=%s", c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	LockOrder(orderId)
	defer UnlockOrder(orderId)

	topUp := model.GetTopUpByTradeNo(orderId)
	if topUp == nil {
		logger.LogWarn(ctx, fmt.Sprintf("BEPUSDT webhook 订单不存在 order_id=%s client_ip=%s", orderId, c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	if topUp.PaymentProvider != model.PaymentProviderBepUsdt {
		logger.LogWarn(ctx, fmt.Sprintf("BEPUSDT webhook 订单支付网关不匹配 order_id=%s payment_provider=%s client_ip=%s", orderId, topUp.PaymentProvider, c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	err = model.RechargeBepUsdt(orderId, c.ClientIP())
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("BEPUSDT webhook 充值失败 order_id=%s client_ip=%s error=%q", orderId, c.ClientIP(), err.Error()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("BEPUSDT webhook 充值成功 order_id=%s trade_id=%s token=%s block_tx=%s actual_amount=%s client_ip=%s", orderId, tradeId, token, blockTxId, actualAmount, c.ClientIP()))
	_, _ = c.Writer.Write([]byte("ok"))
}

func bepUsdtSign(data map[string]interface{}, token string) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		if k == "signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		v := data[k]
		if v == nil {
			continue
		}
		s := fmt.Sprintf("%v", v)
		if s == "" {
			continue
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(s)
		sb.WriteString("&")
	}
	signStr := strings.TrimRight(sb.String(), "&") + token
	return fmt.Sprintf("%x", md5.Sum([]byte(signStr)))
}

func getBepUsdtPayMoney(amount float64, group string) float64 {
	originalAmount := amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		amount = amount / common.QuotaPerUnit
	}
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(originalAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	return amount * setting.BepUsdtUnitPrice * topupGroupRatio * discount
}

func getBepUsdtMinTopup() int64 {
	minTopup := setting.BepUsdtMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		minTopup = minTopup * int(common.QuotaPerUnit)
	}
	return int64(minTopup)
}
