// fpager vibro: alert launched by the fpager daemon over RPC (app_start).
//
// Args: "<level> [<tones file>]", level = info|ok|fail|none.
//   Vibration (+ LED) pattern per level, forced even in Stealth mode; none = no vibration.
//   Optional tone file: lines "<freq_hz> <duration_ms> [<volume 0..1>]", 0 Hz = rest, '#' = comment.
//   Tones play on the speaker at the same time; they are skipped in Stealth mode.
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
    if(p) {
        const char* args = p;
        const char* sp = strchr(args, ' ');
        size_t ln = sp ? (size_t)(sp - args) : strlen(args);
        if(ln < sizeof(level)) {
            memcpy(level, args, ln);
            level[ln] = '\0';
        }
        if(sp) strlcpy(path, sp + 1, sizeof(path));
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
    }

    NotificationApp* notification = furi_record_open(RECORD_NOTIFICATION);
    if(seq) notification_message(notification, seq); // vibration runs on the notification thread
    play_tones(tones, n);
    notification_message_block(notification, &sequence_empty); // wait for vibration to finish
    furi_record_close(RECORD_NOTIFICATION);
    return 0;
}
