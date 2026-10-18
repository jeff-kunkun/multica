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

static void WriteState(void) {
    CGRect b = CGDisplayBounds(gDisplay.displayID);
    NSDictionary *s = @{@"displayID": @(gDisplay.displayID), @"pid": @(getpid()),
                        @"x": @(b.origin.x), @"y": @(b.origin.y), @"width": @(b.size.width), @"height": @(b.size.height)};
    [[NSJSONSerialization dataWithJSONObject:s options:0 error:nil] writeToFile:StatePath() atomically:YES];
}

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        CGDirectDisplayID mainBefore = CGMainDisplayID();
        CGRect mainBounds = CGDisplayBounds(mainBefore);
        gDisplay = [[VDVirtualDisplay alloc] initWithSerialNumber:0x56443201];
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
