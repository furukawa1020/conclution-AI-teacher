package nativevoice

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"strings"
	"sync"

	"google.golang.org/genai"
)

type connectResult struct {
	session ProviderSession
	err     error
}

type receiveResult struct {
	message *genai.LiveServerMessage
	err     error
}

// Open establishes the provider connection and waits for SetupComplete before
// returning. The SDK's WebSocket dial can outlive context cancellation until
// its bounded handshake timeout; any session returned after our deadline is
// closed by the dialing goroutine.
func (s *Service) Open(ctx context.Context) (Session, error) {
	return s.open(ctx, s.config.SystemPrompt)
}

// OpenWithContext creates the same one-turn session while binding one prior
// exchange into setup as escaped JSON data.
func (s *Service) OpenWithContext(
	ctx context.Context,
	conversationContext string,
) (Session, error) {
	systemPrompt, err := s.contextualSystemPrompt(conversationContext)
	if err != nil {
		return nil, err
	}
	return s.open(ctx, systemPrompt)
}

func (s *Service) open(ctx context.Context, systemPrompt string) (Session, error) {
	if ctx == nil {
		return nil, errors.New("native voice open context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, safeContextError("open", err)
	}

	setupCtx, cancelSetup := context.WithTimeout(ctx, s.config.SetupTimeout)
	defer cancelSetup()

	raw, err := s.connect(setupCtx, systemPrompt)
	if err != nil {
		return nil, err
	}
	if err := awaitSetupComplete(setupCtx, raw); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = raw.Close()
		return nil, safeContextError("open", err)
	}

	sessionCtx, cancelSession := context.WithTimeout(ctx, s.config.SessionTimeout)
	session := &liveSession{
		config:  s.config,
		raw:     raw,
		ctx:     sessionCtx,
		cancel:  cancelSession,
		writeCh: make(chan writeRequest),
		notify:  make(chan struct{}),
	}
	go session.writerLoop()
	go session.receiverLoop()
	go func() {
		<-sessionCtx.Done()
		session.terminate(safeContextError("session", sessionCtx.Err()))
	}()
	return session, nil
}

func (s *Service) connect(
	ctx context.Context,
	systemPrompt string,
) (ProviderSession, error) {
	var attemptMu sync.Mutex
	abandoned := false
	resultCh := make(chan connectResult, 1)
	go func() {
		session, err := s.dialer.Connect(
			ctx,
			s.config.Model,
			s.connectConfig(systemPrompt),
		)
		attemptMu.Lock()
		defer attemptMu.Unlock()
		if abandoned {
			if session != nil {
				_ = session.Close()
			}
			return
		}
		resultCh <- connectResult{session: session, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil || result.session == nil {
			if result.session != nil {
				_ = result.session.Close()
			}
			return nil, fmt.Errorf("connect native voice provider: %w", ErrProvider)
		}
		return result.session, nil
	case <-ctx.Done():
		attemptMu.Lock()
		abandoned = true
		select {
		case result := <-resultCh:
			if result.session != nil {
				_ = result.session.Close()
			}
		default:
		}
		attemptMu.Unlock()
		return nil, safeContextError("connect", ctx.Err())
	}
}

func awaitSetupComplete(ctx context.Context, raw ProviderSession) error {
	resultCh := make(chan receiveResult, 1)
	go func() {
		message, err := raw.Receive()
		resultCh <- receiveResult{message: message, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return fmt.Errorf("receive native voice setup: %w", ErrProvider)
		}
		if !isSetupComplete(result.message) {
			scrubProviderMessage(result.message)
			return fmt.Errorf("receive native voice setup: %w", ErrProtocol)
		}
		scrubProviderMessage(result.message)
		return nil
	case <-ctx.Done():
		_ = raw.Close()
		return safeContextError("setup", ctx.Err())
	}
}

func isSetupComplete(message *genai.LiveServerMessage) bool {
	if message == nil || message.SetupComplete == nil {
		return false
	}
	return message.ServerContent == nil &&
		message.ToolCall == nil &&
		message.ToolCallCancellation == nil &&
		message.SessionResumptionUpdate == nil
}

type writeRequest struct {
	ctx    context.Context
	input  genai.LiveRealtimeInput
	result chan error
}

type liveSession struct {
	config Config
	raw    ProviderSession
	ctx    context.Context
	cancel context.CancelFunc

	writeCh chan writeRequest

	sendMu          sync.Mutex
	inputActive     bool
	activityStarted bool
	activityEnded   bool
	inputBytes      int

	queueMu               sync.Mutex
	ready                 []Event
	held                  []Event
	bufferedBytes         int
	outputCommitted       bool
	outputDiscarded       bool
	inputCaptionFinal     bool
	inputCaptionDelivered bool
	terminalErr           error
	notify                chan struct{}

	receiveOutputBytes     int
	receiveTranscriptBytes int

	closeOnce sync.Once
}

func (s *liveSession) StartActivity(ctx context.Context) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := s.currentTerminalError(); err != nil {
		return err
	}
	if s.inputActive || s.activityStarted {
		return ErrActivityState
	}

	// A native provider session intentionally handles one browser turn, so late
	// transcription cannot be mistaken for a later turn.
	s.resetOutputGate()
	if err := s.sendProvider(ctx, genai.LiveRealtimeInput{
		ActivityStart: &genai.ActivityStart{},
	}); err != nil {
		return err
	}
	s.inputActive = true
	s.activityStarted = true
	s.activityEnded = false
	return nil
}

func (s *liveSession) SendPCM20ms(ctx context.Context, pcm []byte) error {
	if len(pcm) != InputFrameBytes {
		return ErrPCMFrameSize
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := s.currentTerminalError(); err != nil {
		return err
	}
	if !s.inputActive {
		return ErrActivityState
	}
	if s.inputBytes > s.config.MaxInputBytes-len(pcm) {
		s.terminate(ErrInputLimit)
		return ErrInputLimit
	}

	owned := append([]byte(nil), pcm...)
	request := genai.LiveRealtimeInput{
		Audio: &genai.Blob{
			Data:     owned,
			MIMEType: InputAudioMIMEType,
		},
	}
	if err := s.sendProvider(ctx, request); err != nil {
		return err
	}
	s.inputBytes += len(pcm)
	return nil
}

// EndActivity is the synchronous provider-write boundary used by the Native
// latency waterfall. A nil return means SendRealtimeInput(ActivityEnd) has
// completed; provider transcription finalization remains a later event.
func (s *liveSession) EndActivity(ctx context.Context) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := s.currentTerminalError(); err != nil {
		return err
	}
	if !s.inputActive {
		return ErrActivityState
	}
	if err := s.sendProvider(ctx, genai.LiveRealtimeInput{
		ActivityEnd: &genai.ActivityEnd{},
	}); err != nil {
		return err
	}
	s.inputActive = false
	s.activityEnded = true
	return nil
}

// CommitOutput releases already-buffered model audio, output captions and turn
// completion, and lets subsequent output flow directly to Receive. It only
// proves that provider input transcription is final; the caller remains
// responsible for completing its deterministic risk and route checks first.
func (s *liveSession) CommitOutput() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.terminalErr != nil {
		return s.terminalErr
	}
	if s.inputActive || !s.activityEnded {
		return ErrActivityState
	}
	if s.outputDiscarded {
		return ErrActivityState
	}
	if !s.inputCaptionFinal || !s.inputCaptionDelivered {
		return ErrInputCaptionPending
	}
	if s.outputCommitted {
		return nil
	}
	s.outputCommitted = true
	if len(s.held) > 0 {
		s.ready = append(s.ready, s.held...)
		clear(s.held)
		s.held = nil
		s.signalLocked()
	}
	return nil
}

// DiscardOutput zeroizes all unconsumed model output while preserving input
// captions and interruption signals that are already ready for Receive.
func (s *liveSession) DiscardOutput() {
	s.queueMu.Lock()
	s.discardOutputLocked()
	s.outputCommitted = false
	s.outputDiscarded = true
	s.queueMu.Unlock()
}

func (s *liveSession) Receive(ctx context.Context) (Event, error) {
	if ctx == nil {
		return Event{}, errors.New("native voice receive context is required")
	}
	for {
		s.queueMu.Lock()
		if len(s.ready) > 0 {
			event := s.ready[0]
			s.ready[0] = Event{}
			s.ready = s.ready[1:]
			s.bufferedBytes -= eventSize(event)
			if event.Kind == EventInputCaption && event.CaptionFinal {
				s.inputCaptionDelivered = true
			}
			s.queueMu.Unlock()
			return event, nil
		}
		if s.terminalErr != nil {
			err := s.terminalErr
			s.queueMu.Unlock()
			return Event{}, err
		}
		notify := s.notify
		s.queueMu.Unlock()

		select {
		case <-ctx.Done():
			return Event{}, safeContextError("receive", ctx.Err())
		case <-notify:
		}
	}
}

func (s *liveSession) Close() error {
	s.terminate(ErrClosed)
	return nil
}

func (s *liveSession) writerLoop() {
	for {
		select {
		case <-s.ctx.Done():
			s.drainWrites()
			return
		case request := <-s.writeCh:
			if err := request.ctx.Err(); err != nil {
				zeroizeRealtimeInput(&request.input)
				request.result <- safeContextError("send", err)
				continue
			}
			err := s.raw.SendRealtimeInput(request.input)
			zeroizeRealtimeInput(&request.input)
			if err != nil {
				request.result <- fmt.Errorf("send native voice provider input: %w", ErrProvider)
				s.terminate(ErrProvider)
				s.drainWrites()
				return
			}
			request.result <- nil
		}
	}
}

func (s *liveSession) sendProvider(
	ctx context.Context,
	input genai.LiveRealtimeInput,
) error {
	if ctx == nil {
		zeroizeRealtimeInput(&input)
		return errors.New("native voice send context is required")
	}
	if err := ctx.Err(); err != nil {
		zeroizeRealtimeInput(&input)
		return safeContextError("send", err)
	}
	if err := s.currentTerminalError(); err != nil {
		zeroizeRealtimeInput(&input)
		return err
	}

	operationCtx, cancel := context.WithTimeout(ctx, s.config.SendTimeout)
	defer cancel()
	stopDeadline := context.AfterFunc(operationCtx, func() {
		s.terminate(safeContextError("send", operationCtx.Err()))
	})
	defer stopDeadline()

	request := writeRequest{
		ctx:    operationCtx,
		input:  input,
		result: make(chan error, 1),
	}
	select {
	case s.writeCh <- request:
		// Ownership of any request bytes has moved to the writer.
	case <-operationCtx.Done():
		zeroizeRealtimeInput(&input)
		return safeContextError("send", operationCtx.Err())
	case <-s.ctx.Done():
		zeroizeRealtimeInput(&input)
		return s.currentTerminalError()
	}

	select {
	case err := <-request.result:
		return err
	case <-operationCtx.Done():
		return safeContextError("send", operationCtx.Err())
	case <-s.ctx.Done():
		return s.currentTerminalError()
	}
}

func (s *liveSession) drainWrites() {
	for {
		select {
		case request := <-s.writeCh:
			zeroizeRealtimeInput(&request.input)
			request.result <- ErrClosed
		default:
			return
		}
	}
}

func zeroizeRealtimeInput(input *genai.LiveRealtimeInput) {
	if input == nil {
		return
	}
	if input.Audio != nil {
		clear(input.Audio.Data)
		input.Audio.Data = nil
	}
	if input.Media != nil {
		clear(input.Media.Data)
		input.Media.Data = nil
	}
	if input.Video != nil {
		clear(input.Video.Data)
		input.Video.Data = nil
	}
}

func (s *liveSession) receiverLoop() {
	for {
		message, err := s.raw.Receive()
		if err != nil {
			if s.ctx.Err() != nil {
				s.terminate(safeContextError("receive", s.ctx.Err()))
			} else {
				s.terminate(ErrProvider)
			}
			return
		}
		events, interrupted, err := s.decodeMessage(message)
		if err != nil {
			clearEvents(events)
			s.terminate(err)
			return
		}
		if len(events) == 0 {
			continue
		}
		if err := s.enqueueEvents(events, interrupted); err != nil {
			clearEvents(events)
			s.terminate(err)
			return
		}
	}
}

func (s *liveSession) decodeMessage(
	message *genai.LiveServerMessage,
) ([]Event, bool, error) {
	defer scrubProviderMessage(message)
	if message == nil {
		return nil, false, ErrProtocol
	}
	if message.SetupComplete != nil {
		return nil, false, ErrProtocol
	}
	if message.ToolCall != nil || message.ToolCallCancellation != nil ||
		message.SessionResumptionUpdate != nil {
		return nil, false, ErrUnexpectedFeature
	}
	if message.GoAway != nil {
		return nil, false, ErrProvider
	}
	content := message.ServerContent
	if content == nil {
		return nil, false, nil
	}

	events := make([]Event, 0, 4)
	if content.InputTranscription != nil {
		event, err := s.transcriptionEvent(EventInputCaption, content.InputTranscription)
		if err != nil {
			return nil, false, err
		}
		if event != nil {
			events = append(events, *event)
		}
	}

	if content.Interrupted {
		events = append(events, Event{Kind: EventInterrupted, Route: RouteNativeAudio})
		return events, true, nil
	}

	if content.OutputTranscription != nil {
		event, err := s.transcriptionEvent(EventOutputCaption, content.OutputTranscription)
		if err != nil {
			clearEvents(events)
			return nil, false, err
		}
		if event != nil {
			events = append(events, *event)
		}
	}

	if content.ModelTurn != nil {
		for _, part := range content.ModelTurn.Parts {
			if part == nil {
				continue
			}
			if hasDisabledPartContent(part) {
				clearEvents(events)
				return nil, false, ErrProtocol
			}
			text := part.Text
			if strings.TrimSpace(text) != "" {
				if part.InlineData != nil {
					clearEvents(events)
					return nil, false, ErrProtocol
				}
				if containsOutputCaption(events, text) {
					continue
				}
				event, err := s.transcriptionTextEvent(
					EventOutputCaption,
					text,
					content.TurnComplete,
				)
				if err != nil {
					clearEvents(events)
					return nil, false, err
				}
				events = append(events, *event)
				continue
			}
			if part.InlineData == nil {
				clearEvents(events)
				return nil, false, ErrProtocol
			}
			blob := part.InlineData
			if !validOutputAudioMIMEType(blob.MIMEType) || len(blob.Data) == 0 || len(blob.Data)%2 != 0 {
				clearEvents(events)
				return nil, false, ErrProtocol
			}
			if len(blob.Data) > s.config.MaxOutputChunkBytes ||
				s.receiveOutputBytes > s.config.MaxOutputBytes-len(blob.Data) {
				clearEvents(events)
				return nil, false, ErrOutputLimit
			}
			s.receiveOutputBytes += len(blob.Data)
			events = append(events, Event{
				Kind:            EventAudioPCM,
				Route:           RouteNativeAudio,
				PCM:             append([]byte(nil), blob.Data...),
				SampleRateHertz: OutputSampleRateHertz,
			})
		}
	}

	if content.TurnComplete {
		events = append(events, Event{
			Kind:               EventTurnComplete,
			Route:              RouteNativeAudio,
			TurnCompleteReason: string(content.TurnCompleteReason),
		})
	}
	return events, false, nil
}

func (s *liveSession) transcriptionEvent(
	kind EventKind,
	transcription *genai.Transcription,
) (*Event, error) {
	if transcription == nil {
		return nil, nil
	}
	if transcription.Text == "" && !transcription.Finished {
		return nil, nil
	}
	return s.transcriptionTextEvent(kind, transcription.Text, transcription.Finished)
}

func (s *liveSession) transcriptionTextEvent(
	kind EventKind,
	text string,
	finished bool,
) (*Event, error) {
	caption := []byte(text)
	if s.receiveTranscriptBytes > s.config.MaxTranscriptBytes-len(caption) {
		clear(caption)
		return nil, ErrTranscriptLimit
	}
	s.receiveTranscriptBytes += len(caption)
	return &Event{
		Kind:         kind,
		Route:        RouteNativeAudio,
		CaptionUTF8:  caption,
		CaptionFinal: finished,
	}, nil
}

func hasDisabledPartContent(part *genai.Part) bool {
	return part.MediaResolution != nil ||
		part.CodeExecutionResult != nil ||
		part.ExecutableCode != nil ||
		part.FileData != nil ||
		part.FunctionCall != nil ||
		part.FunctionResponse != nil ||
		part.Thought ||
		len(part.ThoughtSignature) != 0 ||
		part.VideoMetadata != nil ||
		part.ToolCall != nil ||
		part.ToolResponse != nil ||
		len(part.PartMetadata) != 0
}

func containsOutputCaption(events []Event, text string) bool {
	for _, event := range events {
		if event.Kind != EventOutputCaption || len(event.CaptionUTF8) != len(text) {
			continue
		}
		matches := true
		for index, value := range event.CaptionUTF8 {
			if value != text[index] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func validOutputAudioMIMEType(value string) bool {
	mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != "audio/pcm" {
		return false
	}
	// Vertex Live's documented output contract fixes Native Audio at raw
	// 16-bit little-endian PCM, 24 kHz. Current GA responses may omit the
	// redundant rate parameter and send the canonical media type alone.
	if len(parameters) == 0 {
		return true
	}
	return len(parameters) == 1 && parameters["rate"] == "24000"
}

func (s *liveSession) enqueueEvents(events []Event, interrupted bool) error {
	s.queueMu.Lock()
	if s.terminalErr != nil {
		s.queueMu.Unlock()
		return s.terminalErr
	}
	if interrupted {
		s.discardOutputLocked()
		s.outputCommitted = false
		s.outputDiscarded = true
	}

	if s.outputDiscarded {
		kept := events[:0]
		for index := range events {
			if isGatedOutput(events[index].Kind) {
				events[index].Clear()
				continue
			}
			kept = append(kept, events[index])
		}
		clear(events[len(kept):])
		events = kept
	}
	for _, event := range events {
		if event.Kind == EventInputCaption && event.CaptionFinal {
			s.inputCaptionFinal = true
		}
	}

	batchBytes := 0
	for _, event := range events {
		batchBytes += eventSize(event)
	}
	if len(s.ready)+len(s.held)+len(events) > s.config.MaxPendingEvents ||
		s.bufferedBytes > s.config.MaxPendingBytes-batchBytes {
		s.queueMu.Unlock()
		return ErrPendingLimit
	}

	for _, event := range events {
		if isGatedOutput(event.Kind) && !s.outputCommitted {
			s.held = append(s.held, event)
		} else {
			s.ready = append(s.ready, event)
		}
	}
	s.bufferedBytes += batchBytes
	s.signalLocked()
	s.queueMu.Unlock()
	return nil
}

func (s *liveSession) resetOutputGate() {
	s.queueMu.Lock()
	s.discardOutputLocked()
	s.outputCommitted = false
	s.outputDiscarded = false
	s.inputCaptionFinal = false
	s.inputCaptionDelivered = false
	s.queueMu.Unlock()
}

func (s *liveSession) discardOutputLocked() {
	if len(s.held) > 0 {
		for index := range s.held {
			s.bufferedBytes -= eventSize(s.held[index])
			s.held[index].Clear()
		}
		clear(s.held)
		s.held = nil
	}

	kept := s.ready[:0]
	for index := range s.ready {
		if isGatedOutput(s.ready[index].Kind) {
			s.bufferedBytes -= eventSize(s.ready[index])
			s.ready[index].Clear()
			continue
		}
		kept = append(kept, s.ready[index])
	}
	clear(s.ready[len(kept):])
	s.ready = kept
}

func isGatedOutput(kind EventKind) bool {
	return kind == EventAudioPCM ||
		kind == EventOutputCaption ||
		kind == EventTurnComplete
}

func eventSize(event Event) int {
	return len(event.PCM) + len(event.CaptionUTF8)
}

func clearEvents(events []Event) {
	for index := range events {
		events[index].Clear()
	}
	clear(events)
}

func (s *liveSession) currentTerminalError() error {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.terminalErr == nil {
		return nil
	}
	return s.terminalErr
}

func (s *liveSession) terminate(err error) {
	if err == nil {
		err = ErrClosed
	}
	s.closeOnce.Do(func() {
		s.queueMu.Lock()
		s.terminalErr = err
		clearEvents(s.ready)
		clearEvents(s.held)
		s.ready = nil
		s.held = nil
		s.bufferedBytes = 0
		s.signalLocked()
		s.queueMu.Unlock()

		s.cancel()
		_ = s.raw.Close()
	})
}

func (s *liveSession) signalLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

func scrubProviderMessage(message *genai.LiveServerMessage) {
	if message == nil {
		return
	}
	defer func() {
		message.SetupComplete = nil
		message.ServerContent = nil
		message.ToolCall = nil
		message.ToolCallCancellation = nil
		message.UsageMetadata = nil
		message.GoAway = nil
		message.SessionResumptionUpdate = nil
		message.VoiceActivityDetectionSignal = nil
		message.VoiceActivity = nil
	}()
	if message.ToolCall != nil {
		for _, call := range message.ToolCall.FunctionCalls {
			if call == nil {
				continue
			}
			clear(call.Args)
			call.Args = nil
			call.ID = ""
			call.Name = ""
			call.PartialArgs = nil
		}
	}
	if message.ServerContent == nil {
		return
	}
	content := message.ServerContent
	if content.InputTranscription != nil {
		content.InputTranscription.Text = ""
	}
	if content.OutputTranscription != nil {
		content.OutputTranscription.Text = ""
	}
	if content.ModelTurn == nil {
		return
	}
	for _, part := range content.ModelTurn.Parts {
		if part == nil {
			continue
		}
		part.Text = ""
		clear(part.ThoughtSignature)
		part.ThoughtSignature = nil
		if part.InlineData != nil {
			clear(part.InlineData.Data)
			part.InlineData.Data = nil
		}
		part.MediaResolution = nil
		part.CodeExecutionResult = nil
		part.ExecutableCode = nil
		part.FileData = nil
		part.FunctionCall = nil
		part.FunctionResponse = nil
		part.InlineData = nil
		part.VideoMetadata = nil
		part.ToolCall = nil
		part.ToolResponse = nil
		clear(part.PartMetadata)
		part.PartMetadata = nil
	}
	clear(content.ModelTurn.Parts)
	content.ModelTurn.Parts = nil
	content.ModelTurn = nil
}
