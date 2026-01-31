/**
 * Stream Player
 * Handles HLS playback with session management
 */

(function() {
    'use strict';

    class StreamPlayer {
        constructor(options) {
            this.videoElement = options.videoElement;
            this.playlistUrl = options.playlistUrl;
            this.streamId = options.streamId;
            this.heartbeatInterval = options.heartbeatInterval || 30000; // 30 seconds
            this.onError = options.onError || console.error;
            this.onReady = options.onReady || (() => {});
            
            this.hls = null;
            this.heartbeatTimer = null;
            this.isPlaying = false;
        }

        /**
         * Initialize the player
         */
        async init() {
            try {
                // Check for HLS support
                if (Hls.isSupported()) {
                    this.initHls();
                } else if (this.videoElement.canPlayType('application/vnd.apple.mpegurl')) {
                    // Native HLS support (Safari)
                    this.initNative();
                } else {
                    throw new Error('HLS is not supported in this browser');
                }
            } catch (error) {
                this.onError({
                    type: 'init',
                    message: error.message,
                    error: error
                });
            }
        }

        /**
         * Initialize hls.js player
         */
        initHls() {
            this.hls = new Hls({
                debug: false,
                enableWorker: true,
                lowLatencyMode: true,
                backBufferLength: 90
            });

            // Attach to video element
            this.hls.attachMedia(this.videoElement);

            // Handle events
            this.hls.on(Hls.Events.MEDIA_ATTACHED, () => {
                console.log('HLS media attached');
                this.hls.loadSource(this.playlistUrl);
            });

            this.hls.on(Hls.Events.MANIFEST_PARSED, (event, data) => {
                console.log('HLS manifest parsed, levels:', data.levels.length);
                this.onReady();
                this.startHeartbeat();
            });

            this.hls.on(Hls.Events.ERROR, (event, data) => {
                this.handleHlsError(data);
            });

            // Video element events
            this.videoElement.addEventListener('play', () => {
                this.isPlaying = true;
            });

            this.videoElement.addEventListener('pause', () => {
                this.isPlaying = false;
            });
        }

        /**
         * Initialize native HLS (Safari)
         */
        initNative() {
            this.videoElement.src = this.playlistUrl;
            
            this.videoElement.addEventListener('loadedmetadata', () => {
                console.log('Native HLS loaded');
                this.onReady();
                this.startHeartbeat();
            });

            this.videoElement.addEventListener('error', (e) => {
                this.handleNativeError(e);
            });
        }

        /**
         * Handle HLS.js errors
         */
        handleHlsError(data) {
            console.error('HLS error:', data);

            if (data.fatal) {
                switch (data.type) {
                    case Hls.ErrorTypes.NETWORK_ERROR:
                        // Check for specific HTTP status codes
                        if (data.response && data.response.code) {
                            this.handleHttpError(data.response.code, data.details);
                            return;
                        }
                        // Try to recover
                        console.log('Attempting to recover from network error...');
                        this.hls.startLoad();
                        break;

                    case Hls.ErrorTypes.MEDIA_ERROR:
                        console.log('Attempting to recover from media error...');
                        this.hls.recoverMediaError();
                        break;

                    default:
                        this.destroy();
                        this.onError({
                            type: 'fatal',
                            message: 'Unable to play stream',
                            details: data.details
                        });
                        break;
                }
            }
        }

        /**
         * Handle HTTP error codes
         */
        handleHttpError(statusCode, details) {
            switch (statusCode) {
                case 401:
                    this.onError({
                        type: 'auth',
                        code: 401,
                        message: 'Your session has expired. Please purchase access again.',
                        action: 'redirect_purchase'
                    });
                    break;

                case 403:
                    this.onError({
                        type: 'auth',
                        code: 403,
                        message: 'Access denied. Invalid signature.',
                        action: 'redirect_purchase'
                    });
                    break;

                default:
                    this.onError({
                        type: 'network',
                        code: statusCode,
                        message: 'Stream temporarily unavailable. Please try again.',
                        details: details
                    });
            }
        }

        /**
         * Handle native HLS errors (Safari)
         */
        handleNativeError(event) {
            const error = this.videoElement.error;
            console.error('Native HLS error:', error);
            
            this.onError({
                type: 'native',
                code: error ? error.code : 0,
                message: error ? error.message : 'Unknown playback error'
            });
        }

        /**
         * Start heartbeat to maintain session
         */
        startHeartbeat() {
            if (this.heartbeatTimer) {
                clearInterval(this.heartbeatTimer);
            }

            const sendHeartbeat = async () => {
                if (!this.isPlaying) return;

                try {
                    const response = await fetch(`/api/stream/${this.streamId}/heartbeat`, {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json'
                        },
                        credentials: 'include'
                    });

                    if (!response.ok) {
                        if (response.status === 401) {
                            this.onError({
                                type: 'auth',
                                code: 401,
                                message: 'Session expired.',
                                action: 'redirect_purchase'
                            });
                        }
                        return;
                    }

                    // Parse response - playlist URL is available if needed for recovery
                    const data = await response.json();
                    if (data.playlist_url) {
                        // Store updated URL for potential recovery
                        this.playlistUrl = data.playlist_url;
                    }
                } catch (error) {
                    console.warn('Heartbeat failed:', error);
                }
            };

            // Send initial heartbeat
            sendHeartbeat();

            // Set up interval
            this.heartbeatTimer = setInterval(sendHeartbeat, this.heartbeatInterval);
        }

        /**
         * Play the video
         */
        play() {
            return this.videoElement.play();
        }

        /**
         * Pause the video
         */
        pause() {
            this.videoElement.pause();
        }

        /**
         * Destroy the player and clean up
         */
        destroy() {
            if (this.heartbeatTimer) {
                clearInterval(this.heartbeatTimer);
                this.heartbeatTimer = null;
            }

            if (this.hls) {
                this.hls.destroy();
                this.hls = null;
            }
        }
    }

    // Export to global scope
    window.StreamPlayer = StreamPlayer;
})();
