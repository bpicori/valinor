package llm

import "valinor/internal/event"

func publishCompressionResult(
	publish func(event.EventType, any),
	result CompressionResult,
) {
	if result.Applied {
		publish(event.ContextCompressed, event.ContextCompressedPayload{
			MessagesCompressed: result.MessagesCompressed,
			TokensBefore:       result.TokensBefore,
			TokensAfter:        result.TokensAfter,
		})
		return
	}
	if result.Err != nil {
		publish(event.ContextCompressionFailed, event.ContextCompressionFailedPayload{
			EstimatedTokens: result.TokensBefore,
			ContextLimit:    result.Limit,
			Error:           result.Err.Error(),
		})
	}
}