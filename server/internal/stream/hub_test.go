package stream

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeVoice: users' channels; session 9 may not speak anywhere.
type fakeVoice struct {
	channels map[uint32]uint32
	mute     map[uint32]bool
}

func (f *fakeVoice) Channel(session uint32) (uint32, bool) {
	c, ok := f.channels[session]
	return c, ok
}
func (f *fakeVoice) MaySpeak(session, _ uint32) bool { return session != 9 && !f.mute[session] }

// sink collects what a session's connection gets.
type sink struct {
	frames [][]byte
	texts  []map[string]any
	room   int // frames it takes before it is full (-1: any)
	closed bool
}

func (s *sink) Close() { s.closed = true }

func (s *sink) SendFrame(b []byte) bool {
	if s.room == 0 {
		return false
	}
	s.room--
	s.frames = append(s.frames, b)
	return true
}
func (s *sink) Room() bool { return s.room != 0 }
func (s *sink) SendText(v any) {
	var m map[string]any
	b, _ := json.Marshal(v)
	json.Unmarshal(b, &m)
	s.texts = append(s.texts, m)
}
func (s *sink) types() []string {
	var out []string
	for _, m := range s.texts {
		out = append(out, m["type"].(string))
	}
	return out
}

func setup() (*Hub, *fakeVoice, map[uint32]*sink) {
	v := &fakeVoice{channels: map[uint32]uint32{1: 5, 2: 5, 3: 6, 9: 5}}
	h := New(v)
	sinks := map[uint32]*sink{}
	for _, s := range []uint32{1, 2, 3, 9} {
		sinks[s] = &sink{room: -1}
		h.Connect(s, sinks[s])
	}
	return h, v, sinks
}

func frame(key bool, data byte) []byte {
	f := byte(0)
	if key {
		f = 1
	}
	return []byte{f, 0, 0, 0, 0, 0, 0, 0, 1, data}
}

func TestStartWatchAndFrames(t *testing.T) {
	h, _, sinks := setup()
	var changes [][]Info
	h.OnChange = func(i []Info) { changes = append(changes, i) }
	if err := h.Start(1, Config{Codec: "avc1.42E01F", Width: 1280, Height: 720}); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || !slices.Equal(changes[0], []Info{{Session: 1, Channel: 5}}) {
		t.Fatalf("changes %v", changes)
	}
	if err := h.Watch(3, 1); err == nil {
		t.Fatal("a viewer in another channel watches")
	}
	if err := h.Watch(2, 1); err != nil {
		t.Fatal(err)
	}
	// The viewer gets the stream's config, the streamer is asked for a keyframe.
	if got := sinks[2].types(); !slices.Equal(got, []string{"stream"}) || sinks[2].texts[0]["codec"] != "avc1.42E01F" {
		t.Fatalf("viewer texts %v", sinks[2].texts)
	}
	if got := sinks[1].types(); !slices.Contains(got, "keyframe") {
		t.Fatalf("streamer texts %v", got)
	}
	// Frames before the first keyframe are not sent; then all are, with the
	// streamer's session in front.
	h.Frame(1, frame(false, 7))
	h.Frame(1, frame(true, 8))
	h.Frame(1, frame(false, 9))
	if len(sinks[2].frames) != 2 || sinks[2].frames[0][3] != 1 || sinks[2].frames[0][len(sinks[2].frames[0])-1] != 8 {
		t.Fatalf("viewer frames %v", sinks[2].frames)
	}
	if len(sinks[3].frames) != 0 || len(sinks[9].frames) != 0 {
		t.Fatal("frames to people not watching")
	}
}

func TestStartNeedsSpeakAndVoice(t *testing.T) {
	h, v, _ := setup()
	if err := h.Start(9, Config{Codec: "vp8", Width: 640, Height: 360}); err == nil {
		t.Fatal("started without Speak")
	}
	delete(v.channels, 1)
	if err := h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360}); err == nil {
		t.Fatal("started outside voice")
	}
}

func TestSlowViewersDropFramesUntilAKeyframe(t *testing.T) {
	h, _, sinks := setup()
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	h.Frame(1, frame(true, 1))
	sinks[2].room = 0 // full
	h.Frame(1, frame(false, 2))
	sinks[2].room = -1
	h.Frame(1, frame(false, 3)) // dropped: waits for a keyframe
	h.Frame(1, frame(true, 4))
	var got []byte
	for _, f := range sinks[2].frames {
		got = append(got, f[len(f)-1])
	}
	if !slices.Equal(got, []byte{1, 4}) {
		t.Fatalf("viewer got %v", got)
	}
}

func TestStreamsEndWhenTheStreamerLeaves(t *testing.T) {
	h, _, sinks := setup()
	var last []Info
	h.OnChange = func(i []Info) { last = i }
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	h.Moved(1, 6)
	if len(last) != 0 || !slices.Contains(sinks[2].types(), "ended") {
		t.Fatalf("streams %v, viewer %v", last, sinks[2].types())
	}
	h.Frame(1, frame(true, 1))
	if len(sinks[2].frames) != 0 {
		t.Fatal("frames after the end")
	}
}

func TestViewersWhoLeaveStopWatching(t *testing.T) {
	h, _, sinks := setup()
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	h.Moved(2, 6)
	h.Frame(1, frame(true, 1))
	if len(sinks[2].frames) != 0 {
		t.Fatal("a viewer who left still gets frames")
	}
	h.Disconnect(1, &sink{}) // an old connection: nothing happens
	if len(h.Streams()) != 1 {
		t.Fatal("an old connection ended the stream")
	}
	h.Disconnect(1, sinks[1])
	if len(h.Streams()) != 0 {
		t.Fatal("a stream outlives its connection")
	}
}

func TestStartChecksTheConfig(t *testing.T) {
	h, _, _ := setup()
	for _, c := range []Config{{Codec: strings.Repeat("v", 100), Width: 640, Height: 360}, {Codec: "vp8", Width: 0, Height: 360}, {Codec: "vp8 <x>", Width: 640, Height: 360}, {Codec: "vp8", Width: 99999, Height: 360}} {
		if err := h.Start(1, c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

// A new connection of a session closes the old one (one upload budget per
// session).
func TestANewConnectionReplacesTheOld(t *testing.T) {
	h, _, _ := setup()
	old := &sink{room: -1}
	h.Connect(4, old)
	if !old.closed {
		h.Connect(4, &sink{room: -1})
	}
	if !old.closed {
		t.Fatal("the old connection is still open")
	}
}

// Frames the server dropped (rate limit): viewers wait for a keyframe.
func TestDroppedFramesMakeViewersWaitForAKeyframe(t *testing.T) {
	h, _, sinks := setup()
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	h.Frame(1, frame(true, 1))
	h.Dropped(1)
	h.Frame(1, frame(false, 2))
	h.Frame(1, frame(true, 3))
	if len(sinks[2].frames) != 2 || sinks[2].frames[1][len(sinks[2].frames[1])-1] != 3 {
		t.Fatalf("viewer frames %v", sinks[2].frames)
	}
}

// A viewer asks for a keyframe (its decoder is new); only viewers may.
func TestViewersAskForKeyframes(t *testing.T) {
	h, _, sinks := setup()
	h.now = func() time.Time { return time.Unix(100, 0) }
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	before := keyAsks(sinks[1])
	h.now = func() time.Time { return time.Unix(102, 0) }
	h.RequestKey(3, 1) // not a viewer
	if keyAsks(sinks[1]) != before {
		t.Fatal("a non-viewer asked for a keyframe")
	}
	h.RequestKey(2, 1)
	if keyAsks(sinks[1]) != before+1 {
		t.Fatal("the viewer's request did not reach the streamer")
	}
}

func keyAsks(s *sink) int { return strings.Count(strings.Join(s.types(), ","), "keyframe") }

// A request within the limit waits for its end (a new viewer's decoder asks
// right after the watch did; the keyframe for the watch came too early).
func TestKeyframeRequestsWithinTheLimitAreKeptForLater(t *testing.T) {
	h, _, sinks := setup()
	now := time.Unix(100, 0)
	h.now = func() time.Time { return now }
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	if keyAsks(sinks[1]) != 1 {
		t.Fatal("the watch did not ask")
	}
	now = now.Add(100 * time.Millisecond)
	h.RequestKey(2, 1)
	h.Frame(1, frame(false, 1))
	if keyAsks(sinks[1]) != 1 {
		t.Fatal("asked within the limit")
	}
	now = now.Add(keyframeEvery)
	h.Frame(1, frame(false, 2))
	h.Frame(1, frame(false, 3))
	if keyAsks(sinks[1]) != 2 {
		t.Fatalf("asks: %d, want 2", keyAsks(sinks[1]))
	}
}

// A viewer that was full asks for its keyframe once it has room again (a
// keyframe asked for at once would find it still full).
func TestAFullViewerAsksForAKeyframeWhenItHasRoom(t *testing.T) {
	h, _, sinks := setup()
	now := time.Unix(100, 0)
	h.now = func() time.Time { return now }
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	h.Frame(1, frame(true, 1))
	now = now.Add(time.Second)
	sinks[2].room = 0 // full
	h.Frame(1, frame(false, 2))
	h.Frame(1, frame(false, 3))
	if keyAsks(sinks[1]) != 1 {
		t.Fatal("asked while the viewer is full")
	}
	sinks[2].room = -1
	h.Frame(1, frame(false, 4))
	h.Frame(1, frame(false, 5))
	if keyAsks(sinks[1]) != 2 {
		t.Fatalf("asks: %d, want 2", keyAsks(sinks[1]))
	}
	h.Frame(1, frame(true, 6))
	if f := sinks[2].frames[len(sinks[2].frames)-1]; f[len(f)-1] != 6 {
		t.Fatal("the keyframe did not reach the viewer")
	}
	// Full again soon: one slow viewer does not bring keyframes for all
	// twice a second.
	now = now.Add(time.Second)
	sinks[2].room = 0
	h.Frame(1, frame(false, 7))
	sinks[2].room = -1
	h.Frame(1, frame(false, 8))
	if keyAsks(sinks[1]) != 2 {
		t.Fatal("asked again within the viewer's limit")
	}
	now = now.Add(roomAskEvery)
	h.Frame(1, frame(false, 9))
	if keyAsks(sinks[1]) != 3 {
		t.Fatalf("asks: %d, want 3", keyAsks(sinks[1]))
	}
}

func lastViewers(s *sink) []any {
	for i := len(s.texts) - 1; i >= 0; i-- {
		if s.texts[i]["type"] == "viewers" {
			return s.texts[i]["sessions"].([]any)
		}
	}
	return nil
}

func TestTheStreamerSeesItsViewers(t *testing.T) {
	h, v, sinks := setup()
	v.channels[3] = 5
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	h.Watch(3, 1)
	if got := lastViewers(sinks[1]); len(got) != 2 || got[0] != float64(2) || got[1] != float64(3) {
		t.Fatalf("viewers %v", got)
	}
	h.Unwatch(2, 1)
	h.Moved(3, 6)
	if got := lastViewers(sinks[1]); len(got) != 0 {
		t.Fatalf("viewers after leaving %v", got)
	}
}

func TestPreviews(t *testing.T) {
	h, _, _ := setup()
	var got []Info
	h.OnPreview = func(i Info) { got = append(got, i) }
	img := dataURL(320, 180, false)
	if err := h.SetPreview(1, img); err == nil {
		t.Fatal("a preview without a stream")
	}
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	for _, bad := range []string{"data:image/svg+xml;base64,PHN2Zz4=", "javascript:alert(1)", "data:image/jpeg;base64," + strings.Repeat("A", 70000)} {
		if err := h.SetPreview(1, bad); err == nil {
			t.Fatalf("accepted %.40q", bad)
		}
	}
	if err := h.SetPreview(1, img); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Preview != img || got[0].Channel != 5 {
		t.Fatalf("previews %v", got)
	}
	if s := h.Streams(); s[0].Preview != img {
		t.Fatalf("streams %v", s)
	}
}

func audio(data byte) []byte { return []byte{2, 0, 0, 0, 0, 0, 0, 0, 1, data} }

// Sound goes to every viewer, also one that waits for a video keyframe.
func TestAudioFramesReachAllViewers(t *testing.T) {
	h, _, sinks := setup()
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360, Audio: "opus"})
	h.Watch(2, 1)
	h.Frame(1, audio(7))
	h.Frame(1, frame(false, 8)) // video before a keyframe: not sent
	if len(sinks[2].frames) != 1 || sinks[2].frames[0][len(sinks[2].frames[0])-1] != 7 {
		t.Fatalf("viewer frames %v", sinks[2].frames)
	}
	if sinks[2].texts[0]["audio"] != "opus" {
		t.Fatalf("stream text %v", sinks[2].texts[0])
	}
	if err := h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360, Audio: "mp3"}); err == nil {
		t.Fatal("accepted another sound codec")
	}
}

func TestSoundIsPassedOn(t *testing.T) {
	h, _, _ := setup()
	var got [][]byte
	var stamps []uint64
	h.OnSound = func(streamer uint32, opus []byte, ts uint64) {
		got, stamps = append(got, opus), append(stamps, ts)
	}
	var last []Info
	h.OnChange = func(i []Info) { last = i }
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360, Audio: "opus"})
	if len(last) != 1 || !last[0].Audio {
		t.Fatalf("streams %v", last)
	}
	h.Frame(1, audio(7))
	h.Frame(1, frame(true, 8))
	if len(got) != 1 || got[0][0] != 7 || stamps[0] != 1 {
		t.Fatalf("sound %v %v", got, stamps)
	}
}

func TestSoundFramesAreChecked(t *testing.T) {
	h, _, sinks := setup()
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	h.Watch(2, 1)
	h.Frame(1, audio(7)) // the stream has no sound
	if len(sinks[2].frames) != 0 {
		t.Fatal("sound of a stream without sound")
	}
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360, Audio: "opus"})
	big := append([]byte{2, 0, 0, 0, 0, 0, 0, 0, 1}, make([]byte, 2000)...)
	h.Frame(1, big)
	if len(sinks[2].frames) != 0 {
		t.Fatal("an oversized sound frame went out")
	}
}

// A streamer who may no longer speak (muted by an admin, ACL) loses the
// stream.
func TestStreamsEndWhenSpeakIsGone(t *testing.T) {
	h, _, sinks := setup()
	now := time.Unix(100, 0)
	h.now = func() time.Time { return now }
	h.Start(9, Config{Codec: "vp8", Width: 640, Height: 360}) // 9 may not speak
	if len(h.Streams()) != 0 {
		t.Fatal("started")
	}
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	speak := h.voice.(*fakeVoice)
	speak.mute = map[uint32]bool{1: true}
	now = now.Add(3 * time.Second)
	h.Frame(1, frame(true, 1))
	if len(h.Streams()) != 0 || !slices.Contains(sinks[1].types(), "stopped") {
		t.Fatalf("still streaming: %v", sinks[1].types())
	}
}

// A muted streamer who sends only thumbnails (no frames) loses the stream
// too, camera or screen share.
func TestPreviewsEndTheStreamWhenSpeakIsGone(t *testing.T) {
	for _, camera := range []bool{false, true} {
		h, v, sinks := setup()
		var got []Info
		h.OnPreview = func(i Info) { got = append(got, i) }
		h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360, Camera: camera})
		v.mute = map[uint32]bool{1: true}
		if err := h.SetPreview(ID(1, camera), dataURL(320, 180, false)); err == nil {
			t.Error("accepted")
		}
		if len(got) != 0 || len(h.Streams()) != 0 || !slices.Contains(sinks[1].types(), "stopped") {
			t.Fatalf("camera %v: previews %v, streams %v", camera, got, h.Streams())
		}
	}
}

// dataURL: a w x h picture as a JPEG data URL (or a PNG one with the JPEG
// prefix).
func dataURL(w, h int, asPNG bool) string {
	var b bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, w, h))
	if asPNG {
		png.Encode(&b, img)
	} else {
		jpeg.Encode(&b, img, nil)
	}
	return previewPrefix + base64.StdEncoding.EncodeToString(b.Bytes())
}

// Thumbnails are decoded: JPEGs of thumbnail size only.
func TestPreviewsMustBeSmallJPEGs(t *testing.T) {
	h, _, _ := setup()
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	for name, bad := range map[string]string{
		"not base64": previewPrefix + "%%%",
		"not a JPEG": previewPrefix + "/9j/4AAQ",
		"a PNG":      dataURL(320, 180, true),
		"too wide":   dataURL(maxPreviewSide+1, 10, false),
		"too tall":   dataURL(10, maxPreviewSide+1, false),
	} {
		if err := h.SetPreview(1, bad); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	if err := h.SetPreview(1, dataURL(320, 569, false)); err != nil { // a phone's camera
		t.Fatal(err)
	}
}

func TestPreviewsAreLimitedAndCanBeCleared(t *testing.T) {
	h, _, _ := setup()
	now := time.Unix(100, 0)
	h.now = func() time.Time { return now }
	var got []string
	h.OnPreview = func(i Info) { got = append(got, i.Preview) }
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360})
	img := dataURL(320, 180, false)
	h.SetPreview(1, img)
	h.SetPreview(1, img) // too soon: dropped
	now = now.Add(6 * time.Second)
	h.SetPreview(1, "") // previews turned off: cleared at once
	if !slices.Equal(got, []string{img, ""}) || h.Streams()[0].Preview != "" {
		t.Fatalf("previews %q, streams %v", got, h.Streams())
	}
}

// What a full viewer missed is counted (the connections view shows it):
// pictures refused and those skipped until the next keyframe, and sound.
func TestDroppedFramesAreCountedPerViewer(t *testing.T) {
	h, _, sinks := setup()
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360, Audio: "opus"})
	h.Watch(2, 1)
	h.Frame(1, frame(true, 1))
	sinks[2].room = 0
	h.Frame(1, frame(false, 2))                              // refused
	h.Frame(1, []byte{audioFlag, 0, 0, 0, 0, 0, 0, 0, 1, 9}) // sound refused
	sinks[2].room = -1
	h.Frame(1, frame(false, 3)) // skipped: waits for a keyframe
	h.Frame(1, frame(true, 4))
	if d := h.Drops()[2]; d.Pictures != 2 || d.Sound != 1 {
		t.Fatalf("drops %+v", d)
	}
	if _, ok := h.Drops()[3]; ok {
		t.Fatal("a viewer without drops is listed")
	}
	h.Disconnect(2, sinks[2])
	if _, ok := h.Drops()[2]; ok {
		t.Fatal("counts outlive the connection")
	}
}

func TestFramesWithoutViewers(t *testing.T) {
	h, _, sinks := setup()
	h.Start(1, Config{Codec: "vp8", Width: 640, Height: 360, Audio: "opus"})
	var soundSession uint32
	var soundByte byte
	h.OnSound = func(streamer uint32, opus []byte, _ uint64) {
		soundSession = streamer
		if len(opus) > 0 {
			soundByte = opus[0]
		}
	}
	h.Frame(1, frame(true, 1))
	h.Frame(1, frame(false, 2))
	h.Frame(1, audio(7))
	if soundSession != 1 || soundByte != 7 {
		t.Fatalf("sound to mumble: got session %d byte %d", soundSession, soundByte)
	}
	if len(sinks[2].frames) != 0 {
		t.Fatal("frames sent to unwatched sink")
	}
}
