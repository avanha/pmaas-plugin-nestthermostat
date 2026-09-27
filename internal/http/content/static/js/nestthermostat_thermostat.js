function copyThermostatId(button) {
    const id = button.dataset.id;

    if (!id) {
        return;
    }

    navigator.clipboard.writeText(id).then(
        function () {
            const icon = button.querySelector("i");
            const originalClass = icon.className;
            icon.className = "bi bi-check2";
            setTimeout(function () { icon.className = originalClass; }, 1200);
        },
        function (err) {
            console.error("Failed to copy thermostat id:", err);
        }
    );
}
