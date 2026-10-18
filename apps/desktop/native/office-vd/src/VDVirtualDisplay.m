#import "VDVirtualDisplay.h"
#import <dispatch/dispatch.h>
#import <objc/runtime.h>
#import <AppKit/AppKit.h>
#import <dlfcn.h>

NSErrorDomain const VDVirtualDisplayErrorDomain = @"dev.chekusu.yomado.VirtualDisplay";

// Minimal runtime interfaces. The CGVirtualDisplay family is not in the public
// SDK. Signatures are corroborated by Chromium's macOS virtual display utility:
// https://chromium.googlesource.com/chromium/src/+/HEAD/ui/display/mac/test/virtual_display_util_mac.mm
// No implementation code from Chromium is incorporated here.
@interface VDPrivateDescriptor : NSObject
@property(nonatomic) unsigned int vendorID;
@property(nonatomic) unsigned int productID;
@property(nonatomic) unsigned int serialNum;
@property(nonatomic) unsigned int serialNumber;
@property(nonatomic, strong) NSString *name;
@property(nonatomic) CGSize sizeInMillimeters;
@property(nonatomic) unsigned int maxPixelsWide;
@property(nonatomic) unsigned int maxPixelsHigh;
@property(nonatomic) CGPoint redPrimary;
@property(nonatomic) CGPoint greenPrimary;
@property(nonatomic) CGPoint bluePrimary;
@property(nonatomic) CGPoint whitePoint;
@property(nonatomic, strong) id queue;
@property(nonatomic, copy, nullable) id terminationHandler;
@end

@interface VDPrivateSettings : NSObject
@property(nonatomic, strong) NSArray *modes;
@property(nonatomic) unsigned int hiDPI;
@property(nonatomic) unsigned int rotation;
@end

@interface VDPrivateMode : NSObject
- (instancetype)initWithWidth:(unsigned int)width
                       height:(unsigned int)height
                  refreshRate:(double)refreshRate;
@end

@interface VDPrivateDisplay : NSObject
@property(nonatomic, readonly) unsigned int displayID;
- (instancetype)initWithDescriptor:(id)descriptor;
- (BOOL)applySettings:(id)settings;
@end

@interface VDVirtualDisplay ()
@property(nonatomic, strong, nullable) VDPrivateDisplay *retainedDisplay;
@property(nonatomic, readwrite) CGDirectDisplayID displayID;
@property(nonatomic, readwrite) NSUInteger logicalWidth;
@property(nonatomic, readwrite) NSUInteger logicalHeight;
@property(nonatomic, readwrite) BOOL hiDPI;
@property(nonatomic) uint32_t serialNumber;
@end

static BOOL VDFail(NSError **error, NSInteger code, NSString *message) {
    if (error != NULL) {
        *error = [NSError errorWithDomain:VDVirtualDisplayErrorDomain
                                    code:code
                                userInfo:@{ NSLocalizedDescriptionKey: message }];
    }
    return NO;
}

@implementation VDVirtualDisplay

- (instancetype)init {
    return [self initWithSerialNumber:0x56443101];
}

- (instancetype)initWithSerialNumber:(uint32_t)serialNumber {
    self = [super init];
    if (self != nil) {
        _serialNumber = serialNumber == 0 ? 0x56443101 : serialNumber;
    }
    return self;
}

+ (BOOL)isAPIAvailable {
    Class display = NSClassFromString(@"CGVirtualDisplay");
    Class descriptor = NSClassFromString(@"CGVirtualDisplayDescriptor");
    Class mode = NSClassFromString(@"CGVirtualDisplayMode");
    Class settings = NSClassFromString(@"CGVirtualDisplaySettings");
    return display != Nil && descriptor != Nil && mode != Nil && settings != Nil
        && [display instancesRespondToSelector:@selector(initWithDescriptor:)]
        && [display instancesRespondToSelector:@selector(applySettings:)]
        && [display instancesRespondToSelector:@selector(displayID)]
        && [descriptor instancesRespondToSelector:@selector(setMaxPixelsWide:)]
        && [descriptor instancesRespondToSelector:@selector(setMaxPixelsHigh:)]
        && [descriptor instancesRespondToSelector:@selector(setVendorID:)]
        && [descriptor instancesRespondToSelector:@selector(setProductID:)]
        && [descriptor instancesRespondToSelector:@selector(setSerialNum:)]
        && [descriptor instancesRespondToSelector:@selector(setName:)]
        && [descriptor instancesRespondToSelector:@selector(setSizeInMillimeters:)]
        && [descriptor instancesRespondToSelector:@selector(setRedPrimary:)]
        && [descriptor instancesRespondToSelector:@selector(setGreenPrimary:)]
        && [descriptor instancesRespondToSelector:@selector(setBluePrimary:)]
        && [descriptor instancesRespondToSelector:@selector(setWhitePoint:)]
        && [descriptor instancesRespondToSelector:@selector(setQueue:)]
        && [settings instancesRespondToSelector:@selector(setModes:)]
        && [settings instancesRespondToSelector:@selector(setHiDPI:)]
        && [mode instancesRespondToSelector:@selector(initWithWidth:height:refreshRate:)];
}

- (BOOL)isCreated {
    // Read the strong ivar directly. A synthesized object-property getter can
    // leave an autoreleased retain in a caller's long-lived pool, delaying the
    // CGVirtualDisplay object's deallocation after destroy.
    return _retainedDisplay != nil && _displayID != kCGNullDirectDisplay;
}

- (BOOL)createWithWidth:(NSUInteger)width
                height:(NSUInteger)height
                 hiDPI:(BOOL)hiDPI
                  name:(NSString *)name
                 error:(NSError **)error {
    if (![NSThread isMainThread]) {
        return VDFail(error, 1, @"Create the virtual display on the main thread.");
    }
    if (self.isCreated) {
        if (width == self.logicalWidth && height == self.logicalHeight && hiDPI == self.hiDPI) {
            return YES;
        }
        return VDFail(error, 2, @"A virtual display already exists. Restore physical displays and remove it before changing its resolution.");
    }
    if (![self.class isAPIAvailable]) {
        return VDFail(error, 3, @"The private CGVirtualDisplay API is unavailable on this macOS version.");
    }
    NSUInteger multiplier = hiDPI ? 2 : 1;
    if (width < 320 || height < 240 || width > 8192 / multiplier || height > 8192 / multiplier
        || width * height * multiplier * multiplier > 33554432) {
        return VDFail(error, 4, @"Choose a desktop resolution of at least 320 × 240 and no more than 32 million physical pixels.");
    }
    unsigned int pixelWidth = (unsigned int)(width * multiplier);
    unsigned int pixelHeight = (unsigned int)(height * multiplier);

    // Drain private-framework autoreleased temporaries before returning. In a
    // command-line caller, the surrounding pool can otherwise last for the
    // entire process and keep the virtual display alive after destroy.
    // Keep errors strongly outside the inner pool, then hand them back only
    // after it drains; NSError** is otherwise an autoreleasing out-parameter.
    __block NSError *creationError = nil;
    BOOL created = NO;
    @autoreleasepool {
    created = ^BOOL {
    @try {
        VDPrivateDescriptor *descriptor = (VDPrivateDescriptor *)[[NSClassFromString(@"CGVirtualDisplayDescriptor") alloc] init];
        if (descriptor == nil) {
            return VDFail(&creationError, 5, @"macOS could not allocate a virtual-display descriptor.");
        }
        descriptor.name = name.length > 0 ? name : @"Native Display";
        descriptor.queue = dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0);
        // sRGB chromaticity coordinates, plus an arbitrary non-zero vendor ID.
        descriptor.redPrimary = CGPointMake(0.640, 0.330);
        descriptor.greenPrimary = CGPointMake(0.300, 0.600);
        descriptor.bluePrimary = CGPointMake(0.150, 0.060);
        descriptor.whitePoint = CGPointMake(0.3127, 0.3290);
        descriptor.vendorID = 505;
        descriptor.productID = 0x5644;
        descriptor.serialNum = self.serialNumber;
        if ([descriptor respondsToSelector:@selector(setSerialNumber:)]) {
            descriptor.serialNumber = self.serialNumber;
        }
        if ([descriptor respondsToSelector:@selector(setTerminationHandler:)]) {
            descriptor.terminationHandler = nil;
        }
        descriptor.maxPixelsWide = pixelWidth;
        descriptor.maxPixelsHigh = pixelHeight;
        double pixelsPerInch = hiDPI ? 220.0 : 110.0;
        descriptor.sizeInMillimeters = CGSizeMake(25.4 * pixelWidth / pixelsPerInch,
                                                 25.4 * pixelHeight / pixelsPerInch);

        VDPrivateDisplay *display = (VDPrivateDisplay *)[[NSClassFromString(@"CGVirtualDisplay") alloc] initWithDescriptor:descriptor];
        if (display == nil) {
            return VDFail(&creationError, 6, @"WindowServer declined the virtual display. It may need an unlocked graphical login session.");
        }
        VDPrivateMode *mode = (VDPrivateMode *)[[NSClassFromString(@"CGVirtualDisplayMode") alloc]
            initWithWidth:(unsigned int)width height:(unsigned int)height refreshRate:60.0];
        VDPrivateSettings *settings = (VDPrivateSettings *)[[NSClassFromString(@"CGVirtualDisplaySettings") alloc] init];
        if (mode == nil || settings == nil) {
            return VDFail(&creationError, 7, @"macOS could not allocate the virtual display mode.");
        }
        settings.hiDPI = hiDPI ? 1 : 0;
        if ([settings respondsToSelector:@selector(setRotation:)]) {
            settings.rotation = 0;
        }
        settings.modes = @[mode];
        if (![display applySettings:settings]) {
            return VDFail(&creationError, 8, @"macOS rejected the virtual display resolution.");
        }
        if (display.displayID == kCGNullDirectDisplay) {
            return VDFail(&creationError, 9, @"macOS created no usable virtual display ID.");
        }
        self.retainedDisplay = display;
        self.displayID = display.displayID;
        self.logicalWidth = width;
        self.logicalHeight = height;
        self.hiDPI = hiDPI;
        return YES;
    } @catch (NSException *exception) {
        // Undocumented APIs can change. Convert selector/KVC exceptions into an
        // ordinary error so the app keeps its restore controls available.
        return VDFail(&creationError, 10, [NSString stringWithFormat:@"The private virtual-display API changed: %@", exception.reason ?: exception.name]);
    }
    }();
    }
    if (!created && error != NULL) *error = creationError;
    return created;
}

- (void)destroy {
    NSAssert([NSThread isMainThread], @"Destroy the virtual display on the main thread.");
    self.retainedDisplay = nil;
    self.displayID = kCGNullDirectDisplay;
    self.logicalWidth = 0;
    self.logicalHeight = 0;
    self.hiDPI = NO;
}

+ (NSArray<NSDictionary<NSString *,id> *> *)onlineDisplays {
    CGDirectDisplayID ids[64] = {0};
    uint32_t count = 0;
    if (CGGetOnlineDisplayList(64, ids, &count) != kCGErrorSuccess) {
        return @[];
    }
    NSMutableArray<NSDictionary<NSString *,id> *> *result = [NSMutableArray arrayWithCapacity:count];
    NSMutableDictionary<NSNumber *, NSString *> *displayNames = [NSMutableDictionary dictionary];
    for (NSScreen *screen in NSScreen.screens) {
        NSNumber *screenID = screen.deviceDescription[@"NSScreenNumber"];
        if (screenID != nil) {
            displayNames[screenID] = screen.localizedName;
        }
    }
    for (uint32_t index = 0; index < count; index++) {
        CGDirectDisplayID display = ids[index];
        CGRect bounds = CGDisplayBounds(display);
        CGDisplayModeRef mode = CGDisplayCopyDisplayMode(display);
        NSMutableDictionary<NSString *,id> *item = [@{
            @"displayID": @(display),
            @"vendorID": @(CGDisplayVendorNumber(display)),
            @"modelNumber": @(CGDisplayModelNumber(display)),
            @"serialNumber": @(CGDisplaySerialNumber(display)),
            @"isMain": @(CGDisplayIsMain(display)),
            @"isBuiltin": @(CGDisplayIsBuiltin(display)),
            @"isActive": @(CGDisplayIsActive(display)),
            @"isOnline": @(CGDisplayIsOnline(display)),
            @"mirrorOf": @(CGDisplayMirrorsDisplay(display)),
            @"isInMirrorSet": @(CGDisplayIsInMirrorSet(display)),
            @"rotation": @(CGDisplayRotation(display)),
            @"active": @(CGDisplayIsActive(display)),
            @"main": @(CGDisplayIsMain(display)),
            @"builtin": @(CGDisplayIsBuiltin(display)),
            @"vendor": @(CGDisplayVendorNumber(display)),
            @"model": @(CGDisplayModelNumber(display)),
            @"name": displayNames[@(display)] ?: [NSString stringWithFormat:@"Display %u", display],
            @"isVirtual": @([self isVirtualDisplay:display]),
            @"bounds": @{@"x": @(bounds.origin.x), @"y": @(bounds.origin.y),
                          @"width": @(bounds.size.width), @"height": @(bounds.size.height)},
            @"x": @(bounds.origin.x),
            @"y": @(bounds.origin.y),
            @"logicalWidth": @(bounds.size.width),
            @"logicalHeight": @(bounds.size.height)
        } mutableCopy];
        if (mode != NULL) {
            item[@"pixelWidth"] = @(CGDisplayModeGetPixelWidth(mode));
            item[@"pixelHeight"] = @(CGDisplayModeGetPixelHeight(mode));
            item[@"modeWidth"] = @(CGDisplayModeGetWidth(mode));
            item[@"modeHeight"] = @(CGDisplayModeGetHeight(mode));
            item[@"refreshRate"] = @(CGDisplayModeGetRefreshRate(mode));
            CGDisplayModeRelease(mode);
        }
        [result addObject:item];
    }
    return result;
}

+ (BOOL)isVirtualDisplay:(CGDirectDisplayID)displayID {
    // This optional private export is checked at runtime. Never link against it.
    typedef bool (*VDIsVirtualFunction)(CGDirectDisplayID);
    static VDIsVirtualFunction isVirtualFunction = NULL;
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        isVirtualFunction = (VDIsVirtualFunction)dlsym(RTLD_DEFAULT, "CGDisplayIsVirtual");
    });
    if (isVirtualFunction != NULL) {
        return isVirtualFunction(displayID);
    }
    return CGDisplayModelNumber(displayID) == 0x76697274
        || (CGDisplayVendorNumber(displayID) == 505 && CGDisplayModelNumber(displayID) == 0x5644);
}

@end
