export namespace main {
	
	export class ConnectionStatus {
	    connected: boolean;
	    server: string;
	    hasToken: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connected = source["connected"];
	        this.server = source["server"];
	        this.hasToken = source["hasToken"];
	    }
	}
	export class LoginResult {
	    success: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new LoginResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.error = source["error"];
	    }
	}
	export class StatusInfo {
	    fileCount: number;
	    directoryCount: number;
	    totalSize: number;
	    cursor: string;
	    connected: boolean;
	    server: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new StatusInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileCount = source["fileCount"];
	        this.directoryCount = source["directoryCount"];
	        this.totalSize = source["totalSize"];
	        this.cursor = source["cursor"];
	        this.connected = source["connected"];
	        this.server = source["server"];
	        this.error = source["error"];
	    }
	}

}

