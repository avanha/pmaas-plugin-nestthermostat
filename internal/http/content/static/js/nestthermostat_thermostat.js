/*
 * The copy button of a thermostat's id. Plain ES5, like the other scripts, for old browsers (an iPad that stopped at iOS 9, Safari 9): the
 * clipboard API (navigator.clipboard) is newer than they are, and it needs a secure page as well, so where it is not there the id is shown in a
 * box that can be copied from by hand, which is better than a button that throws.
 */
(function (global) {
    "use strict";

    function copyThermostatId(button, env) {
        env = env || {};
        var id = button.dataset.id;

        if (!id) {
            return;
        }

        var clipboard = env.clipboard !== undefined ? env.clipboard : (global.navigator && global.navigator.clipboard);
        var later = env.later || function (fn, ms) {
            global.setTimeout(fn, ms);
        };

        if (!clipboard || typeof clipboard.writeText !== "function") {
            (env.prompt || function (message, value) {
                global.prompt(message, value);
            })("Thermostat id", id);

            return;
        }

        clipboard.writeText(id).then(
            function () {
                var icon = button.querySelector("i");
                var originalClass = icon.className;
                icon.className = "bi bi-check2";
                later(function () {
                    icon.className = originalClass;
                }, 1200);
            },
            function (err) {
                console.error("Failed to copy thermostat id:", err);
            }
        );
    }

    if (typeof module !== "undefined" && module.exports) {
        module.exports = {copyThermostatId: copyThermostatId};
    }

    // The template's button calls it by name.
    global.copyThermostatId = function (button) {
        copyThermostatId(button);
    };
})(typeof window !== "undefined" ? window : globalThis);
