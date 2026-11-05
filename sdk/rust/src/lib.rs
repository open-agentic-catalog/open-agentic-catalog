//! Asynchronous Catalog API client with explicit credentials and bounded requests.
//!
//! The caller owns its Tokio runtime. Calls return one page and never retry,
//! follow referrals or execute resources. See the crate README for installation.
#![doc = include_str!("../README.md")]

pub mod generated;
mod transport;

pub use generated::client::*;
pub use generated::types::*;
pub use transport::{
    ApiError, ApiOpError, BoxFuture, BufferedResponse, Client, Config, Error, Limits, Response,
    TokenProvider, Transport,
};

#[cfg(test)]
mod unit;
