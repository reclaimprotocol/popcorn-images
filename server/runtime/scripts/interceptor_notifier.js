const reclaimInterceptor = {
    /**
     * @type {Object.<string, function(Object, Object): void>}
     */
    listeners: {},
    /**
     * @param {function(Object, Object): void} callback
     * @param {string} name
     */
    addResponseMiddleware: (callback, name = 'default') => {
        reclaimInterceptor.listeners[name ?? 'default'] = callback;
    },
    /**
     * @param {string} name
     */
    removeResponseMiddleware: (name) => {
        delete reclaimInterceptor.listeners[name];
    },
    /**
     * @param {Object} response
     * @param {Object} request
     */
    notifyListeners: (response, request) => {
        for (const name in reclaimInterceptor.listeners) {
            const callback = reclaimInterceptor.listeners[name];
            callback(response, request);
        }
    }
}

window.reclaimInterceptor = reclaimInterceptor;
