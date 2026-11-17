// Every animator can reach the setup directly from the inspector.
async function openAnimationSetup(page) {
  await page.locator('.inspector-panel #setup-menu').waitFor({state:'visible'});
}
module.exports = {openAnimationSetup};
