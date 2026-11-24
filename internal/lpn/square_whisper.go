package lpn

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"cherry/internal/store"
)

// Square 1:1 private whisper (SQUARE::cr_trunk_call_req idx 10 -> SQUARE::rc_trunk_call idx 13).
// Both handshake state transitions (eWHISPER_STEP 0..7) and private chat text flow over these
// two messages; private chat text is echoed only to sender and target and never broadcast.
const (
	squareCrTrunkCall = 10
	squareRcTrunkCall = 13

	squareWhisperStepNone       int32 = 0
	squareWhisperStepRequest    int32 = 1
	squareWhisperStepCancel     int32 = 2
	squareWhisperStepReceive    int32 = 3
	squareWhisperStepDecline    int32 = 4
	squareWhisperStepMeeting    int32 = 5
	squareWhisperStepConnect    int32 = 6
	squareWhisperStepDisconnect int32 = 7

	squareWhisperOK           int32 = 0
	squareWhisperErrNotFound  int32 = 458754 // 0x70002: target not in room / invalid
	squareWhisperErrBusy      int32 = 458755 // 0x70003: target already in another whisper
	squareWhisperErrNotPaired int32 = 458756 // 0x70004: sender and target not paired

	squareWhisperMaxRunes = 200
	squareWhisperMaxBytes = 500
)

// clearWhisperLocked clears p's whisper state and unlinks its partner if that partner still
// points back to p. Caller holds r.mu.
func (r *squareRoom) clearWhisperLocked(p *squarePlayer) {
	partnerAid := p.whisperPartner
	p.whisperStep = squareWhisperStepNone
	p.whisperPartner = 0
	if partnerAid == 0 {
		return
	}
	for _, o := range r.players {
		if o.aid == partnerAid && o.whisperPartner == p.aid {
			o.whisperPartner = 0
			if o.whisperStep == squareWhisperStepRequest ||
				o.whisperStep == squareWhisperStepReceive ||
				o.whisperStep == squareWhisperStepMeeting {
				o.whisperStep = squareWhisperStepNone
			}
			break
		}
	}
}

// playerByAIDLocked finds the active room member with aid. Caller holds r.mu.
func (r *squareRoom) playerByAIDLocked(aid uint64) *squarePlayer {
	for _, o := range r.players {
		if o.aid == aid {
			return o
		}
	}
	return nil
}

// squareTrunkCallBody encodes SQUARE::rc_trunk_call (msgid 13).
func squareTrunkCallBody(sendAid, targetAid, chatSerial uint64, msg string, step, result int32) []byte {
	b := pbVar(nil, 1, sendAid)
	b = pbVar(b, 2, targetAid)
	b = pbVar(b, 3, chatSerial)
	b = pbLen(b, 4, []byte(msg))
	b = pbVar(b, 5, uint64(step))
	return pbVar(b, 6, uint64(result))
}

func sanitizeWhisperMsg(raw []byte) (string, bool) {
	if !utf8.Valid(raw) {
		return "", false
	}
	msg := strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", ""))
	if msg == "" || len(msg) > squareWhisperMaxBytes || utf8.RuneCountInString(msg) > squareWhisperMaxRunes {
		return "", false
	}
	return msg, true
}

func (s *squareSession) handleWhisper(frame, data []byte) error {
	var (
		targetRaw uint64
		hasTarget bool
		msgBytes  []byte
		stepRaw   uint64
		hasStep   bool
	)
	ok := pbScan(data, func(f, v uint64, d []byte) {
		switch f {
		case 1:
			targetRaw, hasTarget = v, true
		case 2:
			msgBytes = d
		case 3:
			stepRaw, hasStep = v, true
		}
	})
	if !ok || !hasStep || stepRaw > uint64(squareWhisperStepDisconnect) {
		s.logger.Printf("SQUARE whisper invalid aid=%d", s.aid)
		return nil
	}
	step := int32(stepRaw)

	if !hasTarget || int64(targetRaw) <= 0 {
		s.logger.Printf("SQUARE whisper bad target aid=%d step=%d", s.aid, step)
		return s.reply(frame, squareRcTrunkCall, squareTrunkCallBody(targetRaw, targetRaw, 0, "", step, squareWhisperErrNotFound))
	}
	targetAid := targetRaw

	// Case A: Self-step update (TargetAid == sender.aid).
	// Never echo back to sender; broadcast to other room members only.
	if targetAid == s.aid {
		s.room.mu.Lock()
		s.player.whisperStep = step
		if step == squareWhisperStepNone {
			s.player.whisperPartner = 0
		}
		s.room.mu.Unlock()
		s.logger.Printf("SQUARE whisper-step aid=%d step=%d", s.aid, step)
		s.room.broadcast(s.logger, s.player, squareRcTrunkCall, squareTrunkCallBody(s.aid, s.aid, 0, "", step, squareWhisperOK))
		return nil
	}

	// Case C: Active whisper chat (step == 6 with non-empty msg).
	// Delivered only to sender (echo) and target; never broadcast to observers.
	if step == squareWhisperStepConnect && len(msgBytes) > 0 {
		msg, valid := sanitizeWhisperMsg(msgBytes)
		if !valid {
			s.logger.Printf("SQUARE whisper chat rejected aid=%d", s.aid)
			return nil
		}
		var (
			target *squarePlayer
			paired bool
			serial uint64
		)
		s.room.mu.Lock()
		target = s.room.playerByAIDLocked(targetAid)
		if target != nil {
			paired = s.player.whisperPartner == targetAid && target.whisperPartner == s.aid
			if paired {
				s.room.chatSerial++
				serial = s.room.chatSerial
			}
		}
		s.room.mu.Unlock()

		if target == nil {
			return s.reply(frame, squareRcTrunkCall, squareTrunkCallBody(targetAid, targetAid, 0, "", step, squareWhisperErrNotFound))
		}
		if !paired {
			return s.reply(frame, squareRcTrunkCall, squareTrunkCallBody(targetAid, targetAid, 0, "", step, squareWhisperErrNotPaired))
		}

		s.logger.Printf("SQUARE whisper-chat aid=%d target=%d serial=%d bytes=%d", s.aid, targetAid, serial, len(msg))
		store.RecordChat(store.ChatEntry{
			Kind:       store.ChatSquare,
			Channel:    strconv.FormatUint(s.square.Theme, 10),
			Sender:     strconv.FormatUint(s.aid, 10),
			SenderName: squareAccountName(s.aid),
			Text:       msg,
		})
		body := squareTrunkCallBody(s.aid, targetAid, serial, msg, squareWhisperStepConnect, squareWhisperOK)
		if err := s.reply(frame, squareRcTrunkCall, body); err != nil {
			return err
		}
		target.send(s.logger, squareRcTrunkCall, body)
		return nil
	}

	// Case B: Peer handshake (msg == "", step in {2, 3, 4, 5, 6, 7}).
	if len(msgBytes) != 0 || step < squareWhisperStepCancel {
		s.logger.Printf("SQUARE whisper ignored aid=%d step=%d bytes=%d", s.aid, step, len(msgBytes))
		return nil
	}

	var (
		target    *squarePlayer
		busy      bool
		serial    uint64
		observers []*squarePlayer
	)
	s.room.mu.Lock()
	target = s.room.playerByAIDLocked(targetAid)
	if target != nil {
		switch step {
		case squareWhisperStepReceive: // 3: incoming peer whisper request
			if target.whisperStep != squareWhisperStepNone && target.whisperPartner != s.aid {
				busy = true
			} else {
				if s.player.whisperPartner != 0 && s.player.whisperPartner != targetAid {
					if old := s.room.playerByAIDLocked(s.player.whisperPartner); old != nil && old.whisperPartner == s.aid {
						old.whisperPartner = 0
					}
				}
				s.player.whisperPartner = targetAid
				if target.whisperPartner == 0 {
					target.whisperPartner = s.aid
				}
			}
		case squareWhisperStepCancel, squareWhisperStepDecline, squareWhisperStepDisconnect:
			s.player.whisperPartner = 0
			if target.whisperPartner == s.aid {
				target.whisperPartner = 0
			}
		case squareWhisperStepMeeting, squareWhisperStepConnect:
			s.player.whisperPartner = targetAid
			target.whisperPartner = s.aid
		}
		if !busy {
			s.room.chatSerial++
			serial = s.room.chatSerial
			if step == squareWhisperStepConnect {
				for _, o := range s.room.players {
					if o != s.player && o != target {
						observers = append(observers, o)
					}
				}
			}
		}
	}
	s.room.mu.Unlock()

	if target == nil {
		return s.reply(frame, squareRcTrunkCall, squareTrunkCallBody(targetAid, targetAid, 0, "", step, squareWhisperErrNotFound))
	}
	if busy {
		return s.reply(frame, squareRcTrunkCall, squareTrunkCallBody(targetAid, targetAid, 0, "", squareWhisperStepDecline, squareWhisperErrBusy))
	}

	s.logger.Printf("SQUARE whisper-peer aid=%d target=%d step=%d serial=%d", s.aid, targetAid, step, serial)
	body := squareTrunkCallBody(s.aid, targetAid, serial, "", step, squareWhisperOK)
	target.send(s.logger, squareRcTrunkCall, body)
	for _, obs := range observers {
		obs.send(s.logger, squareRcTrunkCall, body)
	}
	return nil
}
