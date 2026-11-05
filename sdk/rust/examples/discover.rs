use open_agentic_catalog::{CatalogSearch, Client, Config};
use serde_json::json;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // These are example inputs. The SDK itself does not read the environment.
    let mut config = Config::new(std::env::var("CATALOG_SDK_ORIGIN")?);
    config.token = Some(std::env::var("CATALOG_SDK_TOKEN")?);
    config.allow_loopback_http =
        std::env::var("CATALOG_SDK_ALLOW_LOOPBACK_HTTP").as_deref() == Ok("on");
    if let Ok(path) = std::env::var("CATALOG_SDK_CA_FILE") {
        config.root_certificates.push(std::fs::read(path)?);
    }
    let client = Client::new(config)?;
    let access = client
        .get_current_access(None::<&str>, None, None::<&str>)
        .await?;
    println!("Access status: {}", access.status);
    let query: CatalogSearch = serde_json::from_value(json!({"limit": 10, "origins": ["local"]}))?;
    let response = client.search_catalog(query).await?;
    let page = response.data.ok_or("Expected a Search page")?;
    println!("Matches on this page: {}", page.items.len());
    // Save page.next_cursor and pass it explicitly in the next Search input.
    Ok(())
}
