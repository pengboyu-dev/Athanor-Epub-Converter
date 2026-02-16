export namespace main {
	
	export class ConversionProgress {
	    jobId: string;
	    stage: string;
	    progress: number;
	    message: string;
	    isComplete: boolean;
	    isError: boolean;
	    outputPath?: string;
	    markdownPath?: string;
	    pdfPath?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConversionProgress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.jobId = source["jobId"];
	        this.stage = source["stage"];
	        this.progress = source["progress"];
	        this.message = source["message"];
	        this.isComplete = source["isComplete"];
	        this.isError = source["isError"];
	        this.outputPath = source["outputPath"];
	        this.markdownPath = source["markdownPath"];
	        this.pdfPath = source["pdfPath"];
	    }
	}

}

