/*
 * The "get token" button of the Nest thermostat page: it asks the plugin for the address to authorise it at (the OAuth attempt) and goes
 * there.
 *
 * It runs on old browsers, an iPad that no longer gets updates among them (Safari 9): plain ES5, with no arrow functions, classes, class
 * fields, let or const, async functions or fetch, none of which it has, and a script that uses one of them does not run at all, which is a
 * button that cannot be clicked. Nothing here is slower or worse in a modern browser.
 *
 * The parts are given what they use from outside (the document, how a request is made, where the browser is sent), so that they are tested
 * with fakes: see js_test/nestthermostat_test.js.
 */
(function (global) {
    "use strict";

    // Posts with nothing in it, and calls done(error, status, text), error being set if there was no answer at all.
    function post(url, done) {
        var request = new global.XMLHttpRequest();

        request.open("POST", url, true);

        request.onload = function () {
            done(null, request.status, request.responseText);
        };

        request.onerror = function () {
            done(new Error("the request could not be sent"));
        };

        request.send();
    }

    function NestThermostatStatus(doc, options) {
        options = options || {};
        this.doc = doc;
        this.post = options.post || post;
        this.goTo = options.goTo || function (uri) {
            global.location.href = uri;
        };
        this.getTokenButton = null;
        this.rootElement = null;
    }

    NestThermostatStatus.prototype.init = function () {
        var self = this;

        if (this.initIfReady()) {
            return;
        }

        var onReadyStateChange = function () {
            if (self.initIfReady()) {
                self.doc.removeEventListener("readystatechange", onReadyStateChange);
            }
        };

        this.doc.addEventListener("readystatechange", onReadyStateChange);
    };

    NestThermostatStatus.prototype.initIfReady = function () {
        var self = this;

        if (this.doc.readyState !== "complete") {
            return false;
        }

        this.rootElement = this.doc.querySelector("div.entity-nestthermostat-status");
        this.getTokenButton = this.rootElement ? this.rootElement.querySelector("button.get-token-button") : null;

        if (this.getTokenButton) {
            this.getTokenButton.addEventListener("click", function () {
                self.onGetTokenButtonClick();
            });
        }

        return true;
    };

    NestThermostatStatus.prototype.onGetTokenButtonClick = function () {
        var self = this;

        this.fetchOAuthUrl(function (attempt) {
            if (attempt) {
                self.goTo(attempt.AuthUri);
            }
        });
    };

    // Asks for the address, and calls done with what the plugin answered, or with nothing if it could not.
    NestThermostatStatus.prototype.fetchOAuthUrl = function (done) {
        this.post("/plugins/nestthermostat/oauthAttempt", function (error, status, text) {
            if (error || status < 200 || status > 299) {
                console.error("Failed to fetch OAuth URL");
                done(null);
                return;
            }

            try {
                done(JSON.parse(text));
            } catch (e) {
                console.error("Failed to fetch OAuth URL: the answer was not understood");
                done(null);
            }
        });
    };

    var exported = {NestThermostatStatus: NestThermostatStatus};

    if (typeof module !== "undefined" && module.exports) {
        module.exports = exported;
    }

    if (typeof document !== "undefined" && global.addEventListener) {
        new NestThermostatStatus(document).init();
    }
})(typeof window !== "undefined" ? window : globalThis);
