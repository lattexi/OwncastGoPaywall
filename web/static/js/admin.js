/**
 * Admin Panel JavaScript
 */

(function() {
    'use strict';

    // Confirm delete actions
    document.querySelectorAll('form[data-confirm]').forEach(function(form) {
        form.addEventListener('submit', function(e) {
            if (!confirm(form.dataset.confirm)) {
                e.preventDefault();
            }
        });
    });

    // Auto-refresh viewer counts every 30 seconds
    function refreshViewerCounts() {
        const viewerCounts = document.querySelectorAll('.viewer-count[data-stream-id]');
        viewerCounts.forEach(async function(el) {
            const streamId = el.dataset.streamId;
            try {
                const response = await fetch('/admin/api/streams/' + streamId + '/viewers', {
                    credentials: 'include'
                });
                if (response.ok) {
                    const data = await response.json();
                    el.textContent = data.viewer_count + ' viewers';
                }
            } catch (e) {
                console.error('Failed to fetch viewer count:', e);
            }
        });
    }

    // Refresh every 30 seconds if we have viewer counts on the page
    if (document.querySelector('.viewer-count[data-stream-id]')) {
        setInterval(refreshViewerCounts, 30000);
    }

    // Flash messages auto-dismiss
    document.querySelectorAll('.flash-message').forEach(function(el) {
        setTimeout(function() {
            el.style.opacity = '0';
            setTimeout(function() {
                el.remove();
            }, 300);
        }, 5000);
    });

})();
