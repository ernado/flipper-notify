// fpager vibro: alert launched by the fpager daemon over RPC (app_start).
//
// Args: "<level> [light] [blink:<color>] [vol:<0..1>] [<tones file>]", level = info|ok|fail|none.
//   Vibration (+ LED) pattern per level, forced even in Stealth mode; none = no vibration.
//   light: wake the display backlight (turns off after the user's auto-off delay).
//   blink:<info|ok|fail|off>: after the buzz, leave the LED blinking in that level's color
//     (hardware blink, keeps running after the app exits); off = stop blinking.
//   vol:<0..1>: scale the volume of every tone (default 1).
//   Optional tone file (any other token): lines "<freq_hz> <duration_ms> [<volume 0..1>]",
//     0 Hz = rest, '#' = comment. Tones play during the buzz; skipped in Stealth mode.
// No GUI; exits when done.
#include <furi.h>
#include <furi_hal.h>
#include <notification/notification.h>
#include <notification/notification_messages.h>
#include <storage/storage.h>
#include <toolbox/stream/file_stream.h>
#include <stdlib.h>

#define BUZZ(ms) &message_vibro_on, &message_delay_##ms, &message_vibro_off

#define MAX_TONES 64

// Blink, left running after exit (no reset at the end of the sequence).
#define BLINK_SEQ(name, color)            \
    static const NotificationSequence name = { \
        &message_blink_start_100,         \
        &message_blink_set_color_##color, \
        &message_do_not_reset,            \
        NULL,                             \
    }
BLINK_SEQ(seq_blink_info, blue);
BLINK_SEQ(seq_blink_ok, green);
BLINK_SEQ(seq_blink_fail, red);

static const NotificationSequence seq_blink_stop = {
    &message_blink_stop,
    NULL,
};

// info: one long buzz, blue LED.
static const NotificationSequence seq_info = {
    &message_force_vibro_setting_on,
    &message_blue_255,
    BUZZ(250),
    &message_delay_100,
    &message_blue_0,
    NULL,
};

// ok: two short buzzes, green LED.
static const NotificationSequence seq_ok = {
    &message_force_vibro_setting_on,
    &message_green_255,
    BUZZ(100),
    &message_delay_100,
    BUZZ(100),
    &message_green_0,
    NULL,
};

// fail: three buzzes, red LED.
static const NotificationSequence seq_fail = {
    &message_force_vibro_setting_on,
    &message_red_255,
    BUZZ(250),
    &message_delay_100,
    BUZZ(250),
    &message_delay_100,
    BUZZ(250),
    &message_red_0,
    NULL,
};

typedef struct {
    uint32_t freq;
    uint32_t ms;
    float volume;
} Tone;

static size_t load_tones(const char* path, Tone* tones) {
    size_t n = 0;
    Storage* storage = furi_record_open(RECORD_STORAGE);
    Stream* stream = file_stream_alloc(storage);
    FuriString* line = furi_string_alloc();
    if(file_stream_open(stream, path, FSAM_READ, FSOM_OPEN_EXISTING)) {
        while(n < MAX_TONES && stream_read_line(stream, line)) {
            const char* s = furi_string_get_cstr(line);
            while(*s == ' ' || *s == '\t')
                s++;
            if(*s == '#' || *s == '\0' || *s == '\n' || *s == '\r') continue;
            char* end;
            unsigned long freq = strtoul(s, &end, 10);
            if(end == s) continue;
            char* end2;
            unsigned long ms = strtoul(end, &end2, 10);
            if(ms == 0 || ms > 5000 || freq > 20000) continue;
            float volume = 1.0f;
            if(end2 != end) {
                float v = strtof(end2, NULL);
                if(v > 0.0f && v <= 1.0f) volume = v;
            }
            tones[n].freq = freq;
            tones[n].ms = ms;
            tones[n].volume = volume;
            n++;
        }
    }
    furi_string_free(line);
    file_stream_close(stream);
    stream_free(stream);
    furi_record_close(RECORD_STORAGE);
    return n;
}

static void play_tones(const Tone* tones, size_t n) {
    if(!furi_hal_speaker_acquire(1000)) return;
    for(size_t i = 0; i < n; i++) {
        if(tones[i].freq > 0) {
            furi_hal_speaker_start((float)tones[i].freq, tones[i].volume);
        } else {
            furi_hal_speaker_stop();
        }
        furi_delay_ms(tones[i].ms);
    }
    furi_hal_speaker_stop();
    furi_hal_speaker_release();
}

int32_t fpager_vibro_main(void* p) {
    char level[8] = "info";
    char path[128] = "";
    char blink[8] = ""; // "" = leave blink state alone
    bool light = false;
    float vol = 1.0f;
    if(p) {
        char args[192];
        strlcpy(args, p, sizeof(args));
        bool first = true;
        // Split on spaces in place (strtok_r is not exported by the firmware API).
        for(char* s = args; *s;) {
            while(*s == ' ')
                s++;
            if(!*s) break;
            char* tok = s;
            while(*s && *s != ' ')
                s++;
            if(*s) *s++ = '\0';
            if(first) {
                strlcpy(level, tok, sizeof(level));
                first = false;
            } else if(strcmp(tok, "light") == 0) {
                light = true;
            } else if(strncmp(tok, "vol:", 4) == 0) {
                float v = strtof(tok + 4, NULL);
                if(v >= 0.0f && v <= 1.0f) vol = v;
            } else if(strncmp(tok, "blink:", 6) == 0) {
                strlcpy(blink, tok + 6, sizeof(blink));
            } else {
                strlcpy(path, tok, sizeof(path));
            }
        }
    }

    const NotificationSequence* blink_seq = NULL;
    if(strcmp(blink, "info") == 0) {
        blink_seq = &seq_blink_info;
    } else if(strcmp(blink, "ok") == 0) {
        blink_seq = &seq_blink_ok;
    } else if(strcmp(blink, "fail") == 0) {
        blink_seq = &seq_blink_fail;
    }

    const NotificationSequence* seq = &seq_info;
    if(strcmp(level, "none") == 0) {
        seq = NULL;
    } else if(strcmp(level, "ok") == 0) {
        seq = &seq_ok;
    } else if(strcmp(level, "fail") == 0) {
        seq = &seq_fail;
    }

    static Tone tones[MAX_TONES];
    size_t n = 0;
    if(path[0] && !furi_hal_rtc_is_flag_set(FuriHalRtcFlagStealthMode)) {
        n = load_tones(path, tones);
        for(size_t i = 0; i < n; i++)
            tones[i].volume *= vol;
    }

    NotificationApp* notification = furi_record_open(RECORD_NOTIFICATION);
    // A running hardware blink owns the LED channels: stop it so the buzz LED shows.
    if(blink[0]) notification_message_block(notification, &seq_blink_stop);
    if(light) notification_message(notification, &sequence_display_backlight_on);
    if(seq) notification_message(notification, seq); // vibration runs on the notification thread
    play_tones(tones, n);
    notification_message_block(notification, &sequence_empty); // wait for vibration to finish
    // Blocking: the sequence lives in this app's memory, which is freed on exit.
    if(blink_seq) notification_message_block(notification, blink_seq);
    furi_record_close(RECORD_NOTIFICATION);
    return 0;
}
