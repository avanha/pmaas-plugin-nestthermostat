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

        console.log("NestThermostatStatus initialized");

        return true;
    }

    onLoginButtonClick = async () => {
        console.log("Login button clicked");
        const attempt = await this.fetchOAuthUrl();

        if (attempt) {
            location.href = attempt.AuthUri;
        }
    }

    fetchOAuthUrl = async () => {
        const response = await fetch("/plugins/nestthermostat/oauthAttempt", {
                method: 'POST',
            });

        if (response.ok) {
            console.log("OAuth URL fetched successfully");
        } else {
            console.error("Failed to fetch OAuth URL");
            return;
        }

        return await response.json();
    }
}

let instance = new NestThermostatStatus(document);
instance.init();
