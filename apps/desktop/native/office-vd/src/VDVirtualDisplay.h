#import <Foundation/Foundation.h>
#import <CoreGraphics/CoreGraphics.h>

NS_ASSUME_NONNULL_BEGIN

FOUNDATION_EXPORT NSErrorDomain const VDVirtualDisplayErrorDomain;

/// Owns one virtual display. Keep this object alive for the entire headless session.
/// This bridge uses undocumented macOS CGVirtualDisplay classes and is intended
/// for a local, unsandboxed utility, rather than an App Store app.
@interface VDVirtualDisplay : NSObject

@property(class, nonatomic, readonly) BOOL isAPIAvailable;
@property(nonatomic, readonly) CGDirectDisplayID displayID;
@property(nonatomic, readonly) BOOL isCreated;
@property(nonatomic, readonly) NSUInteger logicalWidth;
@property(nonatomic, readonly) NSUInteger logicalHeight;
@property(nonatomic, readonly) BOOL hiDPI;

/// Stable serial number makes the same display recognizable across app launches.
/// Use a different serial number for each concurrent virtual display.
- (instancetype)initWithSerialNumber:(uint32_t)serialNumber NS_DESIGNATED_INITIALIZER;
- (instancetype)init;

/// width/height are logical desktop dimensions. HiDPI uses twice those dimensions
/// as physical pixels (e.g. 1920 x 1080 logical / 3840 x 2160 physical pixels).
/// Call on the main thread. Does not set the main display or alter physical displays.
- (BOOL)createWithWidth:(NSUInteger)width
                height:(NSUInteger)height
                 hiDPI:(BOOL)hiDPI
                  name:(NSString *)name
                 error:(NSError * _Nullable * _Nullable)error
    NS_SWIFT_NAME(create(width:height:hiDPI:name:));

/// Releases the retained display; WindowServer can remove it asynchronously.
/// Call on the main thread, after restoring any disconnected physical displays.
- (void)destroy;

/// Read-only CoreGraphics inventory; numeric dimensions distinguish desktop
/// coordinates from pixel dimensions. Works even when the private API is absent.
+ (NSArray<NSDictionary<NSString *, id> *> *)onlineDisplays;

/// Uses CGDisplayIsVirtual when exported by the running CoreGraphics framework;
/// otherwise recognizes this utility's own product/vendor and macOS's 'virt'
/// model number. The fallback can leave unrelated virtual displays unrecognized.
+ (BOOL)isVirtualDisplay:(CGDirectDisplayID)displayID;

@end

NS_ASSUME_NONNULL_END
