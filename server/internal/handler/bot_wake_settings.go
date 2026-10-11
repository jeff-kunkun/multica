package handler

import (
	"encoding/json"
	"fmt"
	"math"
)

func validateBotWakeChainSettings(settings any) error {
	values, ok := settings.(map[string]any)
	if !ok {
		return nil
	}
	value, present := values["bot_wake_chain_limit"]
	if !present {
		return nil
	}
	var n float64
	switch value := value.(type) {
	case float64:
		n = value
	case int:
		n = float64(value)
	default:
		return fmt.Errorf("bot_wake_chain_limit must be an integer from 1 to 100")
	}
	if n < 1 || n > 100 || math.Trunc(n) != n {
		return fmt.Errorf("bot_wake_chain_limit must be an integer from 1 to 100")
	}
	return nil
}

// A task may save unrelated settings but cannot change the Bot guard that
// constrains its own origin. Missing values are carried forward as well.
func keepStoredBotWakeChainLimit(incoming any, stored []byte) any {
	values, ok := incoming.(map[string]any)
	if !ok {
		return incoming
	}
	var old map[string]any
	_ = json.Unmarshal(stored, &old)
	if limit, present := old["bot_wake_chain_limit"]; present {
		values["bot_wake_chain_limit"] = limit
	} else {
		delete(values, "bot_wake_chain_limit")
	}
	return values
}
