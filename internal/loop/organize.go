package loop

// Message hygiene applied before a conversation goes on the wire.
//
// A run's history does not stay tidy on its own. Compaction cuts it in the
// middle, a trimmed window can drop half of a tool-call pair, a provider can
// return the same call twice, and a turn can come back empty. Every one of
// those leaves a conversation that is individually plausible and collectively
// invalid - and providers reject the whole request rather than the bad part, so
// the failure arrives as an opaque 400 in the middle of a long unattended run.
//
// The rules here are the ones the TypeScript engine learned the hard way:
//
//   - a tool call and its result must be adjacent, in that order
//   - a call with no result, or a result with no call, must not be sent
//   - a trigger only makes sense as the last message
//   - empty and repeated messages waste context and confuse the model
//
// Organize is deliberately a pure function over the message list rather than
// something the loop does to its own state. The engine's history is the record
// of what happened; this is how that record is presented to a provider.

// Organize repairs a conversation so a provider will accept it.
//
// The order of operations matters: pairs are clustered first, because the
// orphan checks that follow ask whether a partner exists anywhere, and a
// response that has been moved next to its call is no longer an orphan.
func Organize(messages []Message) []Message {
	organized := clusterActivities(messages)
	organized = dropOrphanedActivities(organized)
	organized = dropEmpty(organized)
	organized = dropConsecutiveDuplicates(organized)

	return organized
}

// clusterActivities moves each tool result to sit directly after its call.
//
// Not cosmetic: several providers validate that a tool message immediately
// follows the assistant turn that requested it, and a history rebuilt from a
// log or interleaved with reasoning does not naturally satisfy that.
func clusterActivities(messages []Message) []Message {
	organized := make([]Message, 0, len(messages))

	for index, message := range messages {
		if message.Type != TypeActivity {
			organized = append(organized, message)

			continue
		}

		activity := message.Activity

		if activity == nil || activity.Kind == "" {
			// an activity message with no activity in it describes nothing; it
			// cannot be paired, rendered, or acted on
			continue
		}

		switch activity.Kind {
		case ActivityRequest:
			organized = append(organized, message)

		case ActivityResponse:
			partner := lastPairedIndex(organized, message)

			if partner < 0 {
				// the call it answers is gone - dropped here rather than sent as
				// a result to nothing
				continue
			}

			organized = insertAt(organized, partner+1, message)

		case ActivityTrigger:
			// a trigger says "act now"; anywhere but last it is describing a
			// moment that has already passed
			if hasLaterConversation(messages[index+1:]) {
				continue
			}

			organized = append(organized, message)

		default:
			// an unrecognised activity kind is not something a provider can be
			// asked to interpret
			continue
		}
	}

	return organized
}

// dropOrphanedActivities removes halves of a pair whose partner is missing.
//
// Both directions, because trimming can take either end: a call whose result
// fell outside the window, and a result whose call did.
func dropOrphanedActivities(messages []Message) []Message {
	kept := make([]Message, 0, len(messages))

	for _, message := range messages {
		if message.Type != TypeActivity || message.Activity == nil {
			kept = append(kept, message)

			continue
		}

		switch message.Activity.Kind {
		case ActivityRequest, ActivityResponse:
			if !hasPartner(messages, message) {
				continue
			}
		}

		kept = append(kept, message)
	}

	return kept
}

// dropEmpty removes messages that carry nothing.
//
// A message with no text and no structure costs tokens and tells the model
// nothing. Backstory is exempt: an empty system prompt is a configuration
// choice, and silently dropping it would change which message comes first.
func dropEmpty(messages []Message) []Message {
	kept := make([]Message, 0, len(messages))

	for _, message := range messages {
		if message.Text == "" && message.Activity == nil && message.Type != TypeBackstory {
			continue
		}

		kept = append(kept, message)
	}

	return kept
}

// dropConsecutiveDuplicates collapses a message repeated back to back.
//
// A retried turn or a re-injected notice can land twice. Repetition is also
// what the model imitates, so leaving duplicates in the history actively
// encourages the loop the cycle guards exist to catch.
func dropConsecutiveDuplicates(messages []Message) []Message {
	kept := make([]Message, 0, len(messages))

	for index, message := range messages {
		if index > 0 && sameMessage(messages[index-1], message) {
			continue
		}

		kept = append(kept, message)
	}

	return kept
}

// lastPairedIndex finds the most recent partner for a message.
//
// Searching backwards matters when the same call is made more than once: the
// result belongs to the call that just happened, not the identical one earlier
// in the conversation.
func lastPairedIndex(messages []Message, message Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Activity.IsPair(message.Activity) {
			return index
		}
	}

	return -1
}

// hasPartner reports whether the conversation holds the other half.
func hasPartner(messages []Message, message Message) bool {
	for _, candidate := range messages {
		if candidate.Activity.IsPair(message.Activity) {
			return true
		}
	}

	return false
}

// hasLaterConversation reports whether anything the model would see follows.
func hasLaterConversation(rest []Message) bool {
	for _, message := range rest {
		if message.Type != TypeBackstory {
			return true
		}
	}

	return false
}

// sameMessage compares two messages for the duplicate check.
func sameMessage(a, b Message) bool {
	if a.Type != b.Type || a.Text != b.Text {
		return false
	}

	// activities are never duplicates of one another: two calls with the same
	// arguments are two real calls the model made, and collapsing them would
	// hide exactly the repetition the cycle guards look for
	if a.Type == TypeActivity {
		return false
	}

	return a.Activity == nil && b.Activity == nil
}

// insertAt places a message at an index, shifting the rest along.
func insertAt(messages []Message, index int, message Message) []Message {
	messages = append(messages, Message{})

	copy(messages[index+1:], messages[index:])

	messages[index] = message

	return messages
}
