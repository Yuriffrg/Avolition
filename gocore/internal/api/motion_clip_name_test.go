package api

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQueueMotionNicknameIsFrozenSeparatelyFromFilename(t *testing.T) {
	f := newQueueFixture(t)
	for _, nickname := range []string{"Reload", "ADS_Up", "装填_左", "Main_Reload"} {
		t.Run(nickname, func(t *testing.T) {
			filename := "Main_" + nickname
			saved := f.save(t, with(f.entry, "name", filename, "motionClipName", nickname))
			restarted := newAPI(t, Options{DataDir: f.profile, Deps: withBridge(f.bridge)})
			queue := f.queue(t, restarted)
			if m(queue[len(queue)-1])["motionClipName"] != nickname {
				t.Fatal("Nickname was not persisted")
			}
			ok(t, call(t, restarted, "save_annotation", f.arms, map[string]any{"name": "Later unrelated nickname"}))
			result := okMap(t, call(t, restarted, "export_queue_run", saved["id"], f.output,
				with(f.options, "motionClipName", "Caller must not replace frozen nickname")))
			payload := f.bridge.last(t).Payload
			if payload["name"] != filename || payload["motionClipName"] != nickname || filepath.Base(result["c4dPath"].(string)) != filename+".c4d" {
				t.Fatalf("filename %v, scene %v, nickname %v", result["c4dPath"], payload["name"], payload["motionClipName"])
			}
		})
	}
}

func TestLegacyQueueMotionNicknameRemovesOnlyExactEntityPrefixOnce(t *testing.T) {
	f := newQueueFixture(t)
	for _, row := range []struct{ name, want string }{
		{"Main_reload", "reload"}, {"Main_ADS_Up_日本語", "ADS_Up_日本語"},
		{"Main_Main_Reload", "Main_Reload"}, {"Custom_reload", "Custom_reload"},
		{"main_Reload", "main_Reload"}, {"Main_", "Main_"},
	} {
		saved := f.save(t, with(f.entry, "name", row.name))
		ok(t, call(t, f.api, "export_queue_run", saved["id"], f.output, f.options))
		if got := f.bridge.last(t).Payload["motionClipName"]; got != row.want {
			t.Errorf("%s: got %v, want %s", row.name, got, row.want)
		}
	}
}

func TestDirectExportMotionNicknameDoesNotUseObjectPrefixOrGuessCustomSceneNames(t *testing.T) {
	f := newSelectedFixture(t)
	for _, nickname := range []string{"Reload", "ADS_Up_日本語"} {
		result := okMap(t, call(t, f.api, "export_animation", f.source, nickname, f.output,
			with(f.options, "bakeTextures", false)))
		payload := f.bridge.last(t).Payload
		if payload["motionClipName"] != nickname || filepath.Base(result["c4dPath"].(string)) != "00000000013E_"+nickname+".c4d" {
			t.Fatal("Object filename prefix leaked into nickname", payload["motionClipName"], result["c4dPath"])
		}
	}
	queue := newQueueFixture(t)
	for _, nickname := range []string{"Main_Reload", "Custom_Name_日本語"} {
		ok(t, call(t, queue.api, "export_animation_set", queue.entity, queue.entry["assignments"], nickname, queue.output, queue.options))
		if got := queue.bridge.last(t).Payload["motionClipName"]; got != nickname {
			t.Fatalf("Explicit full scene name changed: %v", got)
		}
	}
}

func TestMalformedMotionNicknameFailsBeforeQueuePersistenceOrExport(t *testing.T) {
	f := newQueueFixture(t)
	for _, value := range []any{nil, 1, "", "  ", "bad\x00name", strings.Repeat("x", 181)} {
		failed(t, call(t, f.api, "export_queue_save", with(f.entry, "motionClipName", value)))
		failed(t, call(t, f.api, "export_animation_set", f.entity, f.entry["assignments"], "Scene", f.output,
			with(f.options, "motionClipName", value)))
	}
	if len(f.queue(t, nil)) != 0 || len(f.bridge.calls) != 0 || exists(f.output) {
		t.Fatal("Malformed name changed queue or produced output")
	}
}
