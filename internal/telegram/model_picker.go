// Package telegram provides interactive model selection for Telegram chats.
package telegram

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"valinor/internal/config"
	"valinor/internal/gateway"
)

const (
	commandModelPickerOpen     = "model_picker_open"
	commandModelPickerProvider = "model_picker_provider"
	commandModelPickerError    = "model_picker_error"

	modelsPerPage = 8

	callbackPrefix       = "mdl:"
	callbackSelectProv   = "mdl:p:"
	callbackSelectModel  = "mdl:m:"
	callbackPage         = "mdl:pg:"
	callbackBack         = "mdl:back"
	callbackCancel       = "mdl:cancel"
)

type pickerStep int

const (
	pickerStepProvider pickerStep = iota
	pickerStepLoading
	pickerStepModel
)

// modelOption is one selectable model in the picker.
type modelOption struct {
	Provider string
	Model    string
	Display  string
}

// modelPicker tracks in-progress model selection for a chat.
type modelPicker struct {
	messageID int64
	step      pickerStep
	provider  string
	models    []modelOption
	page      int
	requestID string
}

func (p *modelPicker) title() string {
	switch p.step {
	case pickerStepProvider:
		return "Select provider"
	case pickerStepLoading:
		if p.provider != "" {
			return fmt.Sprintf("Loading models for %s...", p.provider)
		}
		return "Loading models..."
	case pickerStepModel:
		if p.provider != "" {
			return fmt.Sprintf("Select model (%s)", p.provider)
		}
		return "Select model"
	default:
		return "Select model"
	}
}

func buildProviderKeyboard(providers map[string]config.ProviderConfig, currentProvider string) *InlineKeyboardMarkup {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)

	rows := make([][]InlineKeyboardButton, 0, len(names)+1)
	for _, name := range names {
		label := name
		if name == currentProvider {
			label = "• " + name
		}
		rows = append(rows, []InlineKeyboardButton{{
			Text:         label,
			CallbackData: callbackSelectProv + name,
		}})
	}
	rows = append(rows, []InlineKeyboardButton{{
		Text:         "Cancel",
		CallbackData: callbackCancel,
	}})
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func buildModelKeyboard(models []modelOption, page int, currentProvider, currentModel string) *InlineKeyboardMarkup {
	total := len(models)
	if total == 0 {
		return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
			{Text: "Back", CallbackData: callbackBack},
			{Text: "Cancel", CallbackData: callbackCancel},
		}}}
	}

	totalPages := (total + modelsPerPage - 1) / modelsPerPage
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	start := page * modelsPerPage
	end := start + modelsPerPage
	if end > total {
		end = total
	}

	rows := make([][]InlineKeyboardButton, 0, end-start+2)
	for _, item := range models[start:end] {
		label := item.Display
		if label == "" {
			label = item.Model
		}
		if item.Provider == currentProvider && item.Model == currentModel {
			label = "• " + label
		}
		rows = append(rows, []InlineKeyboardButton{{
			Text:         truncateButtonText(label),
			CallbackData: callbackSelectModel + strconv.Itoa(indexOfModel(models, item)),
		}})
	}

	nav := make([]InlineKeyboardButton, 0, 3)
	if page > 0 {
		nav = append(nav, InlineKeyboardButton{
			Text:         "◀ Prev",
			CallbackData: callbackPage + strconv.Itoa(page-1),
		})
	}
	nav = append(nav, InlineKeyboardButton{
		Text:         fmt.Sprintf("Page %d/%d", page+1, totalPages),
		CallbackData: callbackPage + strconv.Itoa(page),
	})
	if page < totalPages-1 {
		nav = append(nav, InlineKeyboardButton{
			Text:         "Next ▶",
			CallbackData: callbackPage + strconv.Itoa(page+1),
		})
	}
	rows = append(rows, nav)
	rows = append(rows, []InlineKeyboardButton{
		{Text: "Back", CallbackData: callbackBack},
		{Text: "Cancel", CallbackData: callbackCancel},
	})
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func buildModelsFromResult(payload gateway.ProviderModelsResultPayload, providers map[string]config.ProviderConfig) []modelOption {
	seen := map[string]struct{}{}
	items := make([]modelOption, 0, len(payload.Models)+1)

	for _, model := range payload.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		items = append(items, modelOption{
			Provider: payload.Provider,
			Model:    id,
			Display:  strings.TrimSpace(model.DisplayName),
		})
	}

	if providerCfg, ok := providers[payload.Provider]; ok && providerCfg.DefaultModel != "" {
		if _, exists := seen[providerCfg.DefaultModel]; !exists {
			items = append(items, modelOption{
				Provider: payload.Provider,
				Model:    providerCfg.DefaultModel,
				Display:  providerCfg.DefaultModel,
			})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		left := strings.ToLower(displayName(items[i]))
		right := strings.ToLower(displayName(items[j]))
		if left == right {
			return items[i].Model < items[j].Model
		}
		return left < right
	})

	return items
}

func displayName(item modelOption) string {
	if item.Display != "" {
		return item.Display
	}
	return item.Model
}

func indexOfModel(models []modelOption, target modelOption) int {
	for i, item := range models {
		if item.Provider == target.Provider && item.Model == target.Model {
			return i
		}
	}
	return 0
}

func truncateButtonText(text string) string {
	runes := []rune(text)
	if len(runes) <= 40 {
		return text
	}
	return string(runes[:37]) + "..."
}

func sendModelSwitch(bridge *Bridge, currentProvider, provider, model, requestID string) bool {
	if provider != currentProvider {
		return bridge.SendCommand(gateway.CommandCreateNewSession, gateway.CreateNewSessionPayload{
			Provider: provider,
			Model:    model,
		}, requestID)
	}
	return bridge.SendCommand(gateway.CommandChangeProviderModel, gateway.ChangeProviderModelPayload{
		Provider: provider,
		Model:    model,
	}, requestID)
}

func sendRequestProviderModels(bridge *Bridge, provider, requestID string) bool {
	return bridge.SendCommand(gateway.CommandRequestProviderModels, gateway.RequestProviderModelsPayload{
		Provider: provider,
	}, requestID)
}

func parsePickerCallback(data string) (action string, arg string, ok bool) {
	data = strings.TrimSpace(data)
	if !strings.HasPrefix(data, callbackPrefix) {
		return "", "", false
	}
	switch {
	case strings.HasPrefix(data, callbackSelectProv):
		return "provider", strings.TrimPrefix(data, callbackSelectProv), true
	case strings.HasPrefix(data, callbackSelectModel):
		return "model", strings.TrimPrefix(data, callbackSelectModel), true
	case strings.HasPrefix(data, callbackPage):
		return "page", strings.TrimPrefix(data, callbackPage), true
	case data == callbackBack:
		return "back", "", true
	case data == callbackCancel:
		return "cancel", "", true
	default:
		return "", "", false
	}
}

func pickerRenderResult(text string, markup *InlineKeyboardMarkup) *RenderResult {
	return &RenderResult{
		Text:         EscapeMarkdown(text),
		ParseMode:    "MarkdownV2",
		FallbackText: text,
		ReplyMarkup:  markup,
	}
}

func pickerConfirmationText(provider, model string) string {
	return fmt.Sprintf("Switched to %s / %s", provider, model)
}