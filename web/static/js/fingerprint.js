/**
 * Device Fingerprint - Deprecated
 * Device fingerprinting has been removed. This file is kept as a no-op
 * for backwards compatibility.
 */

(function() {
    'use strict';

    // No-op fingerprint - returns a random ID for session tracking only
    window.DeviceFingerprint = {
        get: async function() {
            return 'no-device-tracking';
        },
        generate: async function() {
            return 'no-device-tracking';
        }
    };
})();
