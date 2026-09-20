function nestthermostat_status() {

}

class NestThermostatStatus {
    constructor(doc) {
        this.doc = doc;
        this.rootElement = null;
        this.loginButton = null;
    }

    onReadyStateChange = () => {
        if (this.initIfReady()) {
            document.removeEventListener("readystatechange", this.onReadyStateChange);
        }
    }

    init() {
        if (!this.initIfReady()) {
            document.addEventListener("readystatechange", this.onReadyStateChange);
        }
    }

    initIfReady = () => {
        if (this.doc.readyState !== "complete") {
            return false;
        }
        this.rootElement = this.doc.querySelector("div.entity-nestthermostat-status");
        this.loginButton = this.rootElement.querySelector("button.login-button");
        this.loginButton.addEventListener("click", this.onLoginButtonClick);

        return true;
    }

    onLoginButtonClick = () => {
        console.log("Login button clicked");
    }
}

let instance = new NestThermostatStatus(document);
instance.init();
