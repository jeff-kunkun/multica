// vd-hold: keeps one virtual display ("Office Agents") alive for office agents.
// The display is placed so it only touches the main display at its bottom-right
// corner, so Kun's pointer practically never drifts into it.
// Writes its bounds to <vd home>/state.json ($OFFICE_VD_HOME, default ~/.agents/vd);
// SIGTERM removes the display.
#import <AppKit/AppKit.h>
#import "VDVirtualDisplay.h"

static VDVirtualDisplay *gDisplay;
static NSString *StatePath(void) {
    NSString *home = NSProcessInfo.processInfo.environment[@"OFFICE_VD_HOME"];
    if (home.length == 0) home = [NSHomeDirectory() stringByAppendingPathComponent:@".agents/vd"];
    return [home stringByAppendingPathComponent:@"state.json"];
}

#define OFFICE_VD_SERIAL 0x56443201u

// Some per-ID queries (CGDisplayBounds among them) answer for a display that no longer exists with a
// fallback display's values, which could be Kun's main screen. Membership in the active display list is
// the real existence check (from yomado, Watchdog.m DisplayActive).
static BOOL DisplayActive(CGDirectDisplayID displayID) {
    CGDirectDisplayID ids[64] = {0};
    uint32_t count = 0;
    if (CGGetActiveDisplayList(64, ids, &count) != kCGErrorSuccess) return NO;
    for (uint32_t i = 0; i < count; i++) if (ids[i] == displayID) return YES;
    return NO;
}

// state.json tells agents where to put their windows, so it is only written for a display that is live,
// ours (serial) and not the main display. Otherwise it is removed: agents then have nowhere to go,
// instead of being sent onto Kun's screen.
static void WriteState(void) {
    CGDirectDisplayID d = gDisplay.displayID;
    CGRect b = CGDisplayBounds(d);
    if (!DisplayActive(d) || CGDisplaySerialNumber(d) != OFFICE_VD_SERIAL || CGDisplayIsMain(d) || CGRectIsEmpty(b)) {
        fprintf(stderr, "virtual display %u not usable; removing state.json\n", d);
        [[NSFileManager defaultManager] removeItemAtPath:StatePath() error:nil];
        return;
    }
    NSDictionary *s = @{@"displayID": @(d), @"serial": @(OFFICE_VD_SERIAL), @"pid": @(getpid()),
                        @"x": @(b.origin.x), @"y": @(b.origin.y), @"width": @(b.size.width), @"height": @(b.size.height)};
    [[NSJSONSerialization dataWithJSONObject:s options:0 error:nil] writeToFile:StatePath() atomically:YES];
}

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        CGDirectDisplayID mainBefore = CGMainDisplayID();
        CGRect mainBounds = CGDisplayBounds(mainBefore);
        gDisplay = [[VDVirtualDisplay alloc] initWithSerialNumber:OFFICE_VD_SERIAL];
        NSError *err = nil;
        if (![gDisplay createWithWidth:1920 height:1080 hiDPI:NO name:@"Office Agents" error:&err]) {
            fprintf(stderr, "create failed: %s\n", err.localizedDescription.UTF8String); return 1;
        }
        [NSThread sleepForTimeInterval:2.0];
        CGDisplayConfigRef cfg;
        CGBeginDisplayConfiguration(&cfg);
        CGConfigureDisplayOrigin(cfg, gDisplay.displayID, (int32_t)CGRectGetMaxX(mainBounds), (int32_t)CGRectGetMaxY(mainBounds));
        CGCompleteDisplayConfiguration(cfg, kCGConfigureForSession);
        [NSThread sleepForTimeInterval:1.0];
        if (CGMainDisplayID() != mainBefore) {
            fprintf(stderr, "main display changed; removing virtual display\n");
            [gDisplay destroy]; return 2;
        }
        WriteState();
        CGRect b = CGDisplayBounds(gDisplay.displayID);
        printf("virtual display %u at %.0f,%.0f %.0fx%.0f\n", gDisplay.displayID, b.origin.x, b.origin.y, b.size.width, b.size.height);
        fflush(stdout);
        dispatch_source_t term = dispatch_source_create(DISPATCH_SOURCE_TYPE_SIGNAL, SIGTERM, 0, dispatch_get_main_queue());
        signal(SIGTERM, SIG_IGN);
        dispatch_source_set_event_handler(term, ^{ [gDisplay destroy]; [[NSFileManager defaultManager] removeItemAtPath:StatePath() error:nil]; exit(0); });
        dispatch_resume(term);
        // Re-record bounds if macOS rearranges displays.
        [[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationDidChangeScreenParametersNotification object:nil queue:nil usingBlock:^(NSNotification *n) { WriteState(); }];
        [NSApp run];
    }
    return 0;
}
