mod report;

#[cfg(test)]
mod unit;

use std::{io::Write, process::ExitCode};

#[tokio::main(flavor = "current_thread")]
async fn main() -> ExitCode {
    match execute().await {
        Ok(code) => ExitCode::from(code),
        Err(message) => {
            eprintln!("Catalog report: {message}");
            ExitCode::FAILURE
        }
    }
}

async fn execute() -> Result<u8, String> {
    let Some(options) = report::Options::parse(std::env::args_os().skip(1))? else {
        println!("{}", report::HELP);
        return Ok(0);
    };
    let client = options.client()?;
    let report = report::within_deadline(report::collect(&client, &options)).await?;
    let output = report.render(&options);
    std::io::stdout()
        .lock()
        .write_all(output.as_bytes())
        .map_err(|_| "Could not write the report.".to_owned())?;
    Ok(report.exit_code())
}
