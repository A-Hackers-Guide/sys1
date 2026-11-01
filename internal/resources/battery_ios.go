//go:build ios && cgo

package resources

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>

// From IOKit/ps/IOPowerSources.h, which the iOS SDK ships without headers.
extern CFTypeRef IOPSCopyPowerSourcesInfo(void);
extern CFStringRef IOPSGetProvidingPowerSourceType(CFTypeRef snapshot);

static int onBattery(void) {
	CFTypeRef info = IOPSCopyPowerSourcesInfo();
	if (info == NULL) return 0;
	CFStringRef src = IOPSGetProvidingPowerSourceType(info);
	int battery = src != NULL && CFStringCompare(src, CFSTR("Battery Power"), 0) == kCFCompareEqualTo;
	CFRelease(info);
	return battery;
}
*/
import "C"

// onBattery asks IOKit, as pmset does on a Mac (iOS has no pmset).
func onBattery() bool { return C.onBattery() != 0 }
