package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBepUsdtSign(t *testing.T) {
	data := map[string]interface{}{
		"amount":       42,
		"notify_url":   "http://example.com/notify",
		"order_id":     "20220201030210321",
		"redirect_url": "http://example.com/redirect",
	}
	token := "epusdt_password_xasddawqe"
	sig := bepUsdtSign(data, token)
	require.Equal(t, "1cd4b52df5587cfb1968b0c0c6e156cd", sig)
}

func TestBepUsdtSignSkipsSignatureField(t *testing.T) {
	data := map[string]interface{}{
		"order_id":  "123",
		"amount":    10,
		"signature": "should_be_ignored",
	}
	token := "test"
	sig := bepUsdtSign(data, token)

	dataWithout := map[string]interface{}{
		"order_id": "123",
		"amount":   10,
	}
	sigWithout := bepUsdtSign(dataWithout, token)
	require.Equal(t, sigWithout, sig)
}

func TestBepUsdtSignSkipsEmptyAndNil(t *testing.T) {
	data := map[string]interface{}{
		"order_id": "123",
		"amount":   10,
		"empty":    "",
		"nilval":   nil,
	}
	token := "test"
	sig := bepUsdtSign(data, token)

	dataClean := map[string]interface{}{
		"order_id": "123",
		"amount":   10,
	}
	sigClean := bepUsdtSign(dataClean, token)
	require.Equal(t, sigClean, sig)
}
